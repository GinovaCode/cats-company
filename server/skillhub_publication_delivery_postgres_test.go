package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Actual PG persistence + two independent node-local Hubs. The wire peer here
// is synthetic, NOT a claim that XiaoBa capture/upload has been implemented.
func TestSkillHubPublicationDeliveryPostgresRecovery(t *testing.T) {
	testPublicationPostgresRecovery(t, false)
}

func TestSkillHubPublicationDeliveryPostgresDurableIntake(t *testing.T) {
	testPublicationPostgresRecovery(t, true)
}

func testPublicationPostgresRecovery(t *testing.T, beforeFirstProbe bool) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for publication dispatcher integration")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatal("test PostgreSQL URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal("open test PostgreSQL")
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("cats_publication_dispatch_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scopedDSN := u.String()
	scoped, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal("open scoped test DB")
	}
	t.Cleanup(func() { scoped.Close() })
	open := func() *postgres.Adapter {
		t.Helper()
		a := &postgres.Adapter{}
		if err := a.Open(scopedDSN); err != nil {
			t.Fatal("open test adapter")
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	a := open()
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	actor, err := a.CreateUser(&types.User{Username: "dispatch-owner", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := a.CreateUser(&types.User{Username: "dispatch-bot", AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveBotConfigWithOwner(bot, actor, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Exec(`UPDATE bot_config SET body_id='dispatch-body' WHERE user_id=$1`, bot); err != nil {
		t.Fatal(err)
	}
	shared := newSharedMemoryRuntimeState()
	attach := func(db *postgres.Adapter, node string) (*Hub, *Client, *publicationDeliveryWorker) {
		t.Helper()
		generation, err := db.BumpBotConnectionGeneration(bot)
		if err != nil {
			t.Fatal(err)
		}
		h := NewHubWithRuntime(db, nil, shared, node)
		c := &Client{uid: bot, accountType: types.AccountBot, bodyID: "dispatch-body", installationID: "dispatch-install", send: make(chan []byte, 32)}
		h.addClient(c)
		if _, err := h.bodyLeases.acquire(bot, c.bodyID, c.connectionID); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		h.botConnectionEpochs[bot] = generation
		h.mu.Unlock()
		device, err := h.userDevices.register(actor, RegisterUserDeviceRequest{BotUID: bot, DeviceID: "dispatch-device", BodyID: c.bodyID, InstallationID: c.installationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe)}})
		if err != nil {
			t.Fatal(err)
		}
		h.bindDeviceClient(actor, device, c)
		w := newPublicationDeliveryWorker(h)
		if w == nil {
			t.Fatal("missing dispatcher")
		}
		h.publicationDelivery = w
		w.running.Store(true)
		t.Cleanup(w.cancel)
		return h, c, w
	}
	h1, c1, w1 := attach(a, "dispatch-node-a")
	in := store.SkillHubPublicationEntryInput{ActorUID: actor, BotUID: bot, RequestID: "dispatch-request", Entrypoint: "workspace", Source: store.SkillHubPublicationSource{DeviceID: "dispatch-device", BodyID: "dispatch-body", InstallationID: "dispatch-install", RouteGeneration: 1, WorkspaceGeneration: "workspace-1", LocalSkillID: "local-skill-1"}}
	// More obsolete device/installation entries than the page size must not
	// starve current work; SQL must scope BEFORE applying LIMIT.
	for i := 0; i < 6; i++ {
		old := in
		old.RequestID = fmt.Sprintf("old-dispatch-%d", i)
		if i%2 == 0 {
			old.Source.DeviceID = "old-device"
		} else {
			old.Source.InstallationID = "old-install"
		}
		if _, _, err := a.CreateSkillHubPublicationEntry(context.Background(), old, store.DefaultSkillHubPublicationEntryPolicy()); err != nil {
			t.Fatal(err)
		}
	}
	var entry *store.SkillHubPublicationEntry
	var probe1 *MsgThinToolRPC
	if beforeFirstProbe {
		h1.publicationWorkspaceEntries = true
		owner := &Client{uid: actor, accountType: types.AccountHuman, send: make(chan []byte, 8)}
		h1.addClient(owner)
		h1.handleThinToolRPCRequest(owner, &MsgThinToolRPC{Type: thinToolRPCTypeRequest,
			ToolName: publicationWorkspaceCreateDurable, RequestID: in.RequestID,
			TargetOwnerUserID: formatUID(actor), TargetDeviceID: in.Source.DeviceID,
			Payload: map[string]interface{}{"bot_uid": strconv.FormatInt(bot, 10), "local_skill_id": in.Source.LocalSkillID, "workspace_generation": in.Source.WorkspaceGeneration}})
		reply := publicationIntakeResponse(t, owner)
		if reply.Error != nil || reply.Result.(map[string]interface{})["state"] != "waiting_runtime" {
			t.Fatalf("durable intake: %+v", reply)
		}
		if len(c1.send) != 0 {
			t.Fatal("runtime was probed before durable admission")
		}
		entry, err = a.GetSkillHubPublicationEntryByRequest(context.Background(), actor, in.RequestID)
		if err != nil || entry == nil || entry.Input != in || reply.Result.(map[string]interface{})["entry_request_id"] != entry.ID {
			t.Fatal("admission response not backed by exact PG entry")
		}
		var attempts int
		if err := scoped.QueryRow(`SELECT delivery_attempts FROM skillhub_publication_outbox WHERE entry_id=$1`, entry.ID).Scan(&attempts); err != nil || attempts != 0 {
			t.Fatal("first probe attempted before intake committed")
		}
		h1.removeClient(owner)
	} else {
		entry, _, err = a.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
		if err != nil {
			t.Fatal(err)
		}
		w1.sweep()
		probe1 = deliveryProbe(t, c1)
	}
	// Runtime/node dies before first probe (v2) or before its reply (v1).
	// Recovery has no Owner socket and uses a new PostgreSQL Adapter and Hub.
	w1.cancel()
	h1.removeClient(c1)
	h1.releaseBotBodyLease(c1)
	h1.unbindDeviceClient(c1)
	h1.clearClientRuntimeRoute(c1)
	a2 := open()
	h2, c2, w2 := attach(a2, "dispatch-node-b")
	if !beforeFirstProbe {
		w2.sweep()
		if len(c2.send) != 0 {
			t.Fatal("stole unexpired lease")
		}
		if _, err := scoped.Exec(`UPDATE skillhub_publication_outbox SET lease_until=clock_timestamp()-INTERVAL '1 second' WHERE entry_id=$1`, entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	w2.sweep()
	probe2 := deliveryProbe(t, c2)
	if probe1 != nil && probe1.RequestID == probe2.RequestID {
		t.Fatal("wire ID reused after restart")
	}
	result := func(p *MsgThinToolRPC) *MsgThinToolRPC {
		return &MsgThinToolRPC{Type: thinToolRPCTypeResult, RequestID: p.RequestID, Result: map[string]interface{}{"schema_version": float64(1), "local_skill_id": "local-skill-1", "workspace_generation": "workspace-1"}}
	}
	if probe1 != nil {
		h2.handleThinToolRPCResult(c2, result(probe1))
		deliveryAckStatus(t, c2, 404)
	}
	h2.handleThinToolRPCResult(c1, result(probe2))
	deliveryAckStatus(t, c1, 403)
	h2.handleThinToolRPCResult(c2, result(probe2))
	deliveryProcess(t, w2)
	deliveryAckStatus(t, c2, 200)
	var state, node string
	var attempts, generation int
	if err := scoped.QueryRow(`SELECT delivery_state,lease_node,delivery_attempts,lease_generation FROM skillhub_publication_outbox WHERE entry_id=$1`, entry.ID).Scan(&state, &node, &attempts, &generation); err != nil {
		t.Fatal(err)
	}
	wantAttempts := 2
	if beforeFirstProbe {
		wantAttempts = 1
	}
	if state != "source_verified" || node != "dispatch-node-b" || attempts != wantAttempts || generation != 2 {
		t.Fatalf("recovery: %s %s %d %d", state, node, attempts, generation)
	}
	recovered, err := a2.GetSkillHubPublicationEntryByRequest(context.Background(), actor, in.RequestID)
	if err != nil || recovered == nil || recovered.ID != entry.ID || recovered.Input != in || recovered.State != "waiting_runtime" {
		t.Fatalf("immutable intent changed: %+v %v", recovered, err)
	}
	var count int
	if err := scoped.QueryRow(`SELECT COUNT(*) FROM skillhub_publication_entries`).Scan(&count); err != nil || count != 7 {
		t.Fatal("duplicate entry")
	}
	if err := scoped.QueryRow(`SELECT COUNT(*) FROM skillhub_publication_outbox WHERE delivery_attempts=0`).Scan(&count); err != nil || count != 6 {
		t.Fatal("old device entries consumed or rebound")
	}
	// A cancelled request survives a new Adapter/Hub and is never dispatched.
	cancelInput := in
	cancelInput.RequestID = "cancel-before-probe"
	cancelInput.Source.RouteGeneration = 2
	stopped, _, err := a2.CreateSkillHubPublicationEntry(context.Background(), cancelInput, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.CancelSkillHubPublicationEntry(context.Background(), actor, stopped.ID); err != nil {
		t.Fatal(err)
	}
	w2.sweep()
	if len(c2.send) != 0 {
		t.Fatal("cancelled intent dispatched")
	}
	// The wire check is read-only and may already be in flight when cancelled.
	// Its late success must receive 409 and leave the persisted receipt intact.
	cancelInput.RequestID = "cancel-in-flight"
	stopped, _, err = a2.CreateSkillHubPublicationEntry(context.Background(), cancelInput, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	w2.sweep()
	late := deliveryProbe(t, c2)
	if _, err := a.CancelSkillHubPublicationEntry(context.Background(), actor, stopped.ID); err != nil {
		t.Fatal(err)
	}
	h2.handleThinToolRPCResult(c2, result(late))
	deliveryProcess(t, w2)
	deliveryAckStatus(t, c2, 409)
	status, err := a2.GetSkillHubPublicationSourceStatus(context.Background(), actor, stopped.ID)
	if err != nil || status == nil || status.State != "cancelled" || status.SourceVerifiedAt != nil {
		t.Fatalf("late receipt replaced cancellation: %+v %v", status, err)
	}
	w2.sweep()
	if len(c2.send) != 0 {
		t.Fatal("cancelled intent requeued")
	}
	// Exercise the real private peer handler over the real PG grant/authority
	// path. No grant issuer or Cloud upload consumer is activated by this test.
	_, _, body, _ := peerFixture(t)
	upload := store.SkillHubPublicationUploadInput{EntryID: entry.ID, Binding: store.SkillHubPublicationDeliveryBinding{
		ActorUID: actor, BotUID: bot, Source: in.Source, NodeID: "dispatch-node-b", ConnectionID: c2.connectionID},
		Issuer: "https://cats.example", Audience: "https://hub.example", Action: store.SkillHubPublicationUploadAction,
		Revision: 1, RequestHash: body.RequestHash, SourceSnapshotHash: body.SourceSnapshotHash, RequestBytes: body.RequestBytes}
	upload.Binding.Source.RouteGeneration = 2
	grant, token, err := a2.IssueSkillHubPublicationUploadGrant(context.Background(), upload)
	if err != nil {
		t.Fatal(err)
	}
	body.EntryRequestID = entry.ID
	body.AuthorityEpoch = grant.Epoch
	peer := NewSkillHubPublicationPeerHandlerFromEnv(a2)
	request := peerRequest(t, body)
	request.Header.Set("X-SkillHub-Upload-Grant", token.Reveal())
	response := runPeer(t, peer, request, 200)
	if !strings.Contains(response.Body.String(), `"active":true`) || !strings.Contains(response.Body.String(), `"actorCatsCoUid":"`+strconv.FormatInt(actor, 10)+`"`) {
		t.Fatal("peer failed canonical identity")
	}
	if _, err := a2.CancelSkillHubPublicationEntry(context.Background(), actor, entry.ID); err != nil {
		t.Fatal(err)
	}
	request = peerRequest(t, body)
	request.Header.Set("X-SkillHub-Upload-Grant", token.Reveal())
	response = runPeer(t, peer, request, 200)
	if strings.TrimSpace(response.Body.String()) != `{"active":false,"schemaVersion":1}` {
		t.Fatal("cancelled grant active through peer API")
	}
	// A delayed Cloud receipt is evidence, not a live token check. Exercise
	// actual HTTPS + PostgreSQL after cancellation, then replay after reopening.
	// Cloud's sender is a synthetic request here, not its future outbox worker.
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED", "true")
	callback := publicationCloudReceiptRequest{SchemaVersion: 1, EntryRequestID: entry.ID,
		CandidateRevision: 1, AcceptanceEpoch: grant.Epoch, RequestHash: upload.RequestHash,
		SourceSnapshotHash: upload.SourceSnapshotHash, RequestBytes: upload.RequestBytes,
		DelegationID: grant.ID, IdempotencyKey: grant.IdempotencyKey,
		TaskID: "pubtask_11111111-1111-4111-8111-111111111111", EventID: "pubevent_22222222-2222-4222-8222-222222222222"}
	cloudPath := skillHubPublicationPrefix + "/entries/" + entry.ID + "/cloud-status"
	readCloud := func(db *postgres.Adapter, stage string) *time.Time {
		t.Helper()
		response := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", cloudPath, actor), 200)
		var view struct {
			Cloud publicationCloudStatusView `json:"cloudStatus"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
		if view.Cloud.Stage != stage || view.Cloud.LocalIntentState != "cancelled" || view.Cloud.ExecutionState != "unknown" {
			t.Fatalf("private Cloud observation: %s", response.Body.String())
		}
		for _, hidden := range []string{callback.TaskID, callback.EventID, callback.DelegationID, callback.IdempotencyKey, callback.RequestHash, token.Reveal()} {
			if strings.Contains(response.Body.String(), hidden) {
				t.Fatal("private read leaked internal evidence")
			}
		}
		return view.Cloud.RecordedAt
	}
	if readCloud(a2, "not_observed") != nil {
		t.Fatal("fabricated receipt before callback")
	}
	var recordedAt *time.Time
	for i := 0; i < 2; i++ {
		func() {
			db := open()
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			ts := httptest.NewTLSServer(http.HandlerFunc(NewSkillHubPublicationPeerHandlerFromEnv(db).Handle))
			defer ts.Close()
			data, _ := json.Marshal(callback)
			r, _ := http.NewRequest("POST", ts.URL+SkillHubPublicationCloudReceiptPath, bytes.NewReader(data))
			r.Header.Set("Authorization", "Bearer "+peerSecret)
			r.Header.Set("Content-Type", "application/json")
			resp, err := ts.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			var ack map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&ack); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != 200 || len(ack) != 7 || ack["recorded"] != true || ack["localIntentCancelled"] != true || ack["commitAllowed"] != false || ack["taskId"] != callback.TaskID || ack["eventId"] != callback.EventID {
				t.Fatalf("late receipt: %+v", ack)
			}
			at := readCloud(db, "acceptance_recorded")
			if at == nil || (recordedAt != nil && !at.Equal(*recordedAt)) {
				t.Fatal("receipt read lost stable timestamp")
			}
			recordedAt = at
		}()
	}
	if err := scoped.QueryRow(`SELECT count(*) FROM skillhub_publication_cloud_receipts WHERE entry_id=$1`, entry.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("receipt not durable/idempotent")
	}
	if _, _, err := a2.IssueSkillHubPublicationUploadGrant(context.Background(), upload); err == nil {
		t.Fatal("late callback revived issuance")
	}
}
