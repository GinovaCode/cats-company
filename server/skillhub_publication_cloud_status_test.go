package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type publicationCloudStatusTestStore struct {
	*publicationStatusTestStore
	cloud      *store.SkillHubPublicationCloudStatus
	errCloud   error
	cloudCalls atomic.Int32
	readCloud  func(context.Context, int64, string) (*store.SkillHubPublicationCloudStatus, error)
}

func (s *publicationCloudStatusTestStore) GetSkillHubPublicationCloudStatus(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationCloudStatus, error) {
	s.cloudCalls.Add(1)
	if s.readCloud != nil {
		return s.readCloud(ctx, uid, id)
	}
	return s.cloud, s.errCloud
}

func newPublicationCloudStatusTestStore() *publicationCloudStatusTestStore {
	base := newPublicationStatusTestStore()
	return &publicationCloudStatusTestStore{publicationStatusTestStore: base, cloud: &store.SkillHubPublicationCloudStatus{
		EntryID: publicationReadTestID, ActorUID: 1, LocalIntentState: "waiting_runtime", ObservedAt: base.status.ObservedAt}}
}

const publicationCloudStatusTestPath = skillHubPublicationPrefix + "/entries/" + publicationReadTestID + "/cloud-status"

func TestSkillHubPublicationReadCloudStatusProjection(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		for _, recorded := range []bool{false, true} {
			db := newPublicationCloudStatusTestStore()
			if cancelled {
				db.cloud.LocalIntentState = "cancelled"
			} // cancellation after first entry read
			stage := "not_observed"
			if recorded {
				at := db.cloud.ObservedAt.Add(-time.Second)
				db.cloud.ReceiptRecordedAt = &at
				stage = "acceptance_recorded"
			}
			before := *db.cloud
			rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), 200)
			var got struct {
				SchemaVersion int                        `json:"schemaVersion"`
				EntryID       string                     `json:"entryRequestId"`
				Cloud         publicationCloudStatusView `json:"cloudStatus"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.SchemaVersion != 1 || got.EntryID != publicationReadTestID || got.Cloud.Stage != stage || got.Cloud.ExecutionState != "unknown" || got.Cloud.LocalIntentState != db.cloud.LocalIntentState || got.Cloud.ObservedAt != db.cloud.ObservedAt || (got.Cloud.RecordedAt != nil) != recorded {
				t.Fatalf("projection: %s", rec.Body.String())
			}
			var fields map[string]json.RawMessage
			_ = json.Unmarshal(rec.Body.Bytes(), &fields)
			if len(fields) != 3 {
				t.Fatal("unexpected response field")
			}
			var viewFields map[string]json.RawMessage
			_ = json.Unmarshal(fields["cloudStatus"], &viewFields)
			want := 4
			if recorded {
				want = 5
			}
			if len(viewFields) != want {
				t.Fatal("unexpected cloud field")
			}
			for _, secret := range []string{"private-", "ActorUID", "pubtask_", "pubevent_", "pubgrant_", "candidateHash", "commitAllowed", "published", "token"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("leaked %s", secret)
				}
			}
			if *db.cloud != before || db.entry.State != "waiting_runtime" || db.calls.Load() != 1 || db.cloudCalls.Load() != 1 || db.statusCalls.Load() != 0 {
				t.Fatal("read changed state or called wrong ledger")
			}
		}
	}
}

func TestSkillHubPublicationReadCloudStatusFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*publicationCloudStatusTestStore)
		code   int
	}{
		{"missing", func(s *publicationCloudStatusTestStore) { s.cloud = nil }, 404},
		{"other actor", func(s *publicationCloudStatusTestStore) { s.cloud.ActorUID = 2 }, 404},
		{"other entry", func(s *publicationCloudStatusTestStore) { s.cloud.EntryID = "other" }, 503},
		{"unknown state", func(s *publicationCloudStatusTestStore) { s.cloud.LocalIntentState = "published" }, 503},
		{"missing observation", func(s *publicationCloudStatusTestStore) { s.cloud.ObservedAt = time.Time{} }, 503},
		{"zero timestamp", func(s *publicationCloudStatusTestStore) { s.cloud.ReceiptRecordedAt = &time.Time{} }, 503},
		{"future timestamp", func(s *publicationCloudStatusTestStore) {
			v := s.cloud.ObservedAt.Add(time.Second)
			s.cloud.ReceiptRecordedAt = &v
		}, 503},
		{"database failure", func(s *publicationCloudStatusTestStore) { s.errCloud = errors.New("/private/token") }, 503},
		{"denied", func(s *publicationCloudStatusTestStore) { s.errCloud = store.ErrSkillHubPublicationDenied }, 404},
		{"invalid", func(s *publicationCloudStatusTestStore) { s.errCloud = store.ErrSkillHubPublicationInvalid }, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newPublicationCloudStatusTestStore()
			tc.change(db)
			rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), tc.code)
			if strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "published") {
				t.Fatal("raw error/state leak")
			}
		})
	}
}

func TestSkillHubPublicationReadCloudStatusAccessAndTransport(t *testing.T) {
	for _, tc := range []struct {
		uid  int64
		code int
	}{{0, 401}, {2, 404}, {3, 403}, {4, 403}, {5, 403}} {
		db := newPublicationCloudStatusTestStore()
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationCloudStatusTestPath, tc.uid), tc.code)
		if db.cloudCalls.Load() != 0 {
			t.Fatal("unauthorized ledger read")
		}
	}
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"POST", publicationCloudStatusTestPath, 405}, {"GET", publicationCloudStatusTestPath + "?token=x", 400},
		{"GET", publicationCloudStatusTestPath + "/cancel", 404}, {"GET", strings.Replace(publicationCloudStatusTestPath, "pubentry_", "%70ubentry_", 1), 404},
	} {
		db := newPublicationCloudStatusTestStore()
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, tc.method, tc.path, 1), tc.code)
		if db.cloudCalls.Load() != 0 || db.calls.Load() != 0 {
			t.Fatal("invalid request reached ledger")
		}
	}
	for _, mode := range []string{"body", "duplicate auth", "cookie", "query"} {
		db := newPublicationCloudStatusTestStore()
		r := publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1)
		want := 401
		switch mode {
		case "body":
			r.ContentLength = 1
			want = 400
		case "duplicate auth":
			r.Header.Add("Authorization", r.Header.Get("Authorization"))
		case "cookie":
			r.Header.Set("Cookie", "token="+r.Header.Get("Authorization"))
			r.Header.Del("Authorization")
		case "query":
			r.URL.RawQuery = "token=" + r.Header.Get("Authorization")
			r.Header.Del("Authorization")
		}
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), r, want)
		if db.cloudCalls.Load() != 0 {
			t.Fatal("bad authentication/body reached ledger")
		}
	}
	db := newPublicationCloudStatusTestStore()
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, false), publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), 404)
	if db.cloudCalls.Load() != 0 || db.calls.Load() != 0 {
		t.Fatal("disabled read reached storage")
	}
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db.publicationReadTestStore, true), publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), 503)
	for _, enabled := range []bool{false, true} {
		h := NewSkillHubPublicationReadHandler(db, enabled)
		rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
		var caps map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &caps)
		if caps["cloudStatusReadsEnabled"] != enabled {
			t.Fatal("wrong capability")
		}
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "")
	runPublicationRead(t, NewSkillHubPublicationReadHandlerFromEnv(db), publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), 404)
	h := NewSkillHubPublicationReadHandler(db, true)
	for _, path := range []string{publicationCloudStatusTestPath, publicationStatusTestPath, strings.TrimSuffix(publicationCloudStatusTestPath, "/cloud-status")} {
		runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 1), 200)
	}
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), 429)
	if rec.Header().Get("Retry-After") != "5" {
		t.Fatal("missing polling guidance")
	}
}

func TestSkillHubPublicationReadCloudStatusConcurrency(t *testing.T) {
	db := newPublicationCloudStatusTestStore()
	for i := int64(10); i < 26; i++ {
		db.users[i] = &types.User{ID: i, AccountType: types.AccountHuman}
	}
	db.read = func(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationEntry, error) {
		e := *db.entry
		e.Input.ActorUID = uid
		return &e, nil
	}
	started := make(chan struct{}, 16)
	db.readCloud = func(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationCloudStatus, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			return nil, errors.New("missing budget")
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	h := NewSkillHubPublicationReadHandler(db, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 16)
	for i := int64(10); i < 26; i++ {
		r := publicationReadRequest(t, "GET", publicationCloudStatusTestPath, i).WithContext(ctx)
		go func() { w := httptest.NewRecorder(); h.Handle(w, r); done <- w.Code }()
	}
	for i := 0; i < 16; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("read not started")
		}
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", publicationCloudStatusTestPath, 1), 503)
	cancel()
	for i := 0; i < 16; i++ {
		select {
		case code := <-done:
			if code != 503 {
				t.Fatal("cancelled read succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("read not released")
		}
	}
	if len(h.reads) != 0 || db.cloudCalls.Load() != 16 {
		t.Fatal("slot leak")
	}
}
