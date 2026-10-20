package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Expose the real store interfaces under test, without starting unrelated
// artifact/task reapers against a schema that this test later drops.
type captureTransportPostgresStore struct {
	store.Store
	store.SkillHubPublicationEntryStore
	store.SkillHubPublicationSourceStatusReader
	store.SkillHubPublicationCaptureStore
	store.ConversationTaskGenerationStore
}

// Real Hub routing and PostgreSQL, synthetic runtime reply. This does not claim
// a live XiaoBa capture, uploader, scheduling or end-to-end user acceptance.
func TestSkillHubPublicationCaptureTransportPostgres(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for capture transport persistence")
	}
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatal("test PostgreSQL URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("cats_capture_transport_%d", time.Now().UnixNano())
	if _, err = admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
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
	scoped, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scoped.Close() })
	open := func() *postgres.Adapter {
		a := &postgres.Adapter{}
		if err := a.Open(u.String()); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	a := open()
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	actor, err := a.CreateUser(&types.User{Username: "capture-owner", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := a.CreateUser(&types.User{Username: "capture-bot", AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveBotConfigWithOwner(bot, actor, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Exec(`UPDATE bot_config SET body_id='capture-body' WHERE user_id=$1`, bot); err != nil {
		t.Fatal(err)
	}
	attach := func(db *postgres.Adapter) (*Hub, *Client, store.SkillHubPublicationDeliveryBinding) {
		generation, err := db.BumpBotConnectionGeneration(bot)
		if err != nil {
			t.Fatal(err)
		}
		h := NewHub(&captureTransportPostgresStore{db, db, db, db, db}, nil)
		c := &Client{uid: bot, accountType: types.AccountBot, bodyID: "capture-body", installationID: "capture-install", send: make(chan []byte, 16)}
		h.addClient(c)
		if _, err := h.bodyLeases.acquire(bot, c.bodyID, c.connectionID); err != nil {
			t.Fatal(err)
		}
		h.mu.Lock()
		h.botConnectionEpochs[bot] = generation
		h.mu.Unlock()
		d, err := h.userDevices.register(actor, RegisterUserDeviceRequest{BotUID: bot, DeviceID: "capture-install", BodyID: c.bodyID, InstallationID: c.installationID, RuntimeRole: "server", Status: "online", Capabilities: []string{string(DeviceCapabilitySkillHubPublicationProbe), string(DeviceCapabilitySkillHubPublicationCapture)}})
		if err != nil {
			t.Fatal(err)
		}
		h.bindDeviceClient(actor, d, c)
		h.publicationCapture = newPublicationCaptureTransport(h)
		return h, c, store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: bot, NodeID: h.nodeID, ConnectionID: c.connectionID, Source: store.SkillHubPublicationSource{DeviceID: d.DeviceID, BodyID: d.BodyID, InstallationID: d.InstallationID, RouteGeneration: int64(generation), LocalSkillID: "pubsrc-" + strings.Repeat("a", 48), WorkspaceGeneration: strings.Repeat("b", 64)}}
	}
	h, c, b := attach(a)
	entry, _, err := a.CreateSkillHubPublicationEntry(context.Background(), store.SkillHubPublicationEntryInput{ActorUID: actor, BotUID: bot, Entrypoint: "workspace", RequestID: "capture-transport", Source: b.Source}, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	probe, err := a.ClaimSkillHubPublicationDelivery(context.Background(), entry.ID, b)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.FinishSkillHubPublicationDelivery(context.Background(), *probe, ""); err != nil {
		t.Fatal(err)
	}
	call := func(h *Hub, c *Client, change bool, beforeReply func()) captureCallResult {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		done := make(chan captureCallResult, 1)
		go func() {
			r, err := h.CaptureSkillHubPublication(ctx, actor, entry.ID)
			done <- captureCallResult{r, err}
		}()
		var msg ServerMessage
		select {
		case raw := <-c.send:
			if err := json.Unmarshal(raw, &msg); err != nil {
				t.Fatal(err)
			}
		case r := <-done:
			t.Fatalf("early capture: %v", r.err)
		case <-ctx.Done():
			t.Fatal("missing request")
		}
		reply := captureReply(msg.ThinToolRPC)
		result := reply.Result.(map[string]interface{})
		result["entryId"] = entry.ID
		if change {
			result["requestHash"] = strings.Repeat("e", 64)
		}
		if beforeReply != nil {
			beforeReply()
		}
		h.handleThinToolRPCResult(c, reply)
		r := captureWait(t, done)
		want := 200
		if r.err != nil {
			want = 409
		}
		deliveryAckStatus(t, c, want)
		return r
	}
	first := call(h, c, false, nil)
	if first.err != nil || first.record == nil {
		t.Fatal(first.err)
	}
	// A new adapter and authenticated connection replay the same durable capture.
	a2 := open()
	h2, c2, _ := attach(a2)
	again := call(h2, c2, false, nil)
	if again.err != nil || again.record == nil || *again.record != *first.record {
		t.Fatal("reconnect replaced retained capture", again.err)
	}
	changed := call(h2, c2, true, nil)
	if !errors.Is(changed.err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("candidate substitution accepted", changed.err)
	}
	denied := call(h2, c2, false, func() {
		_, err := a2.RequestSkillHubPublicationCancellation(context.Background(), actor, entry.ID)
		if err != nil {
			t.Fatal(err)
		}
	})
	if denied.err == nil {
		t.Fatal("cancelled entry accepted late receipt")
	}
	var count int
	if err := scoped.QueryRow(`SELECT count(*) FROM skillhub_publication_captures WHERE entry_id=$1`, entry.ID).Scan(&count); err != nil || count != 1 {
		t.Fatal("capture was lost/replaced", count, err)
	}
}
