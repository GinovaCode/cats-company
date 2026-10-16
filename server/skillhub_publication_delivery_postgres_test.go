package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
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
}
