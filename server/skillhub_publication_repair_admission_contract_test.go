package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

// Called by the existing required PostgreSQL/HTTPS/JWT publication contract.
// The rejection is synthetic; this is not a real model/file-repair E2E test.
func verifyHumanRepairAdmissionPostgres(t *testing.T, db *postgres.Adapter, in store.SkillHubPublicationCommitInput, actor int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	local, err := db.GetSkillHubPublicationCommitContext(ctx, actor, in.Receipt.Query.EntryID, true)
	if err != nil || local == nil {
		t.Fatal("repair context", err)
	}
	bot := local.Entry.Input.BotUID
	group, err := db.CreateGroup("publication repair contract", actor)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.UpdateGroupKind(group, "agent_task"); err != nil {
		t.Fatal(err)
	}
	if err := db.AddGroupMember(group, bot, "member"); err != nil {
		t.Fatal(err)
	}
	topic := fmt.Sprintf("grp_%d", group)
	cloud := &humanReportCloud{&humanCommitCloud{in: in}, func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		if r != in.Receipt {
			t.Error("repair admission changed retained receipt")
		}
		return publicationReportFixture(r), nil
	}}
	open := func(a *postgres.Adapter) *SkillHubPublicationHumanCommitHandler {
		h := newPublicationHumanCommitHandler(a, cloud, true)
		h.repairEnabled = true
		return h
	}
	var current atomic.Pointer[SkillHubPublicationHumanCommitHandler]
	current.Store(open(db))
	var lose atomic.Bool
	lose.Store(true)
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := httptest.NewRecorder()
		current.Load().Handle(&permitContractWriter{ResponseWriter: w, record: record}, r)
		if record.Code == 200 && lose.Swap(false) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		for k, v := range record.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(record.Code)
		w.Write(record.Body.Bytes())
	}))
	defer ts.Close()
	call := func(method, body string, want int) []byte {
		t.Helper()
		r := humanCommitRequest(t, "repair-task", body, actor)
		r.Method = method
		r = r.WithContext(ctx)
		r.URL.Path = strings.Replace(r.URL.Path, publicationReadTestID, in.Receipt.Query.EntryID, 1)
		r.URL.Scheme, r.URL.Host, r.RequestURI = "https", strings.TrimPrefix(ts.URL, "https://"), ""
		res, err := ts.Client().Do(r)
		if want == 0 {
			if err == nil {
				res.Body.Close()
				t.Fatal("did not lose admission response")
			}
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("admission %s %d %s %v", method, res.StatusCode, b, err)
		}
		return b
	}
	body := `{"schemaVersion":1,"topicId":"` + topic + `"}`
	call("POST", body, 0)
	first, err := db.GetSkillHubPublicationRepairTask(ctx, actor, in.Receipt.Query.EntryID)
	if err != nil || first == nil || len(first.Rounds) != 0 || first.Input.TopicID != topic {
		t.Fatal("admission did not commit before lost reply", err)
	}
	// A fresh handler proves there is no per-handler replay map. Reopening the
	// database adapter is covered by the ledger's recovery integration tests.
	current.Store(open(db))
	get := call("GET", "", 200)
	replay := call("POST", body, 200)
	if string(get) != string(replay) {
		t.Fatal("replay changed admitted task")
	}
	var projection struct {
		TaskID    string `json:"taskId"`
		Rounds    int    `json:"roundsUsed"`
		Automatic bool   `json:"automaticRepairEnabled"`
	}
	if json.Unmarshal(replay, &projection) != nil || projection.TaskID != first.ID || projection.Rounds != 0 || projection.Automatic {
		t.Fatal("admission spent budget or enabled execution")
	}
	call("POST", `{"schemaVersion":1,"topicId":"grp_9223372036854775807"}`, 409)
	current.Store(open(db))
	if err := db.UpdateGroupKind(group, "standard"); err != nil {
		t.Fatal(err)
	}
	call("POST", body, 403)
	if err := db.UpdateGroupKind(group, "agent_task"); err != nil {
		t.Fatal(err)
	}
	after, err := db.GetSkillHubPublicationCommitContext(ctx, actor, in.Receipt.Query.EntryID, true)
	if err != nil || after == nil || after.Intent != nil || after.Receipt == nil || *after.Receipt != in.Receipt {
		t.Fatal("admission published or changed receipt", err)
	}
	t.Log("real JWT/HTTPS/PostgreSQL repair admission: committed response loss, reconstructed handler read/replay, same zero-round task, topic rebinding rejected, changed task authority denied; synthetic Cloud report, no model/file mutation")
	verifyHumanDescriptionConfirmationPostgres(t, db, in, actor)
}
