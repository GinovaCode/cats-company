package server

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestSkillHubPublicationCancellationCloudContract(t *testing.T) {
	dsn, root := os.Getenv("CATS_PG_TEST_DSN"), os.Getenv("SKILLHUB_CONTROL_CONTRACT_ROOT")
	if dsn == "" || root == "" {
		if os.Getenv("CATSCO_REQUIRE_CANCELLATION_CONTRACT") == "1" {
			t.Fatal("contract dependencies missing")
		}
		t.Skip("requires PostgreSQL and pinned Cloud checkout")
	}
	if expected := os.Getenv("SKILLHUB_CONTROL_CONTRACT_REVISION"); expected != "" {
		out, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
		if err != nil || strings.TrimSpace(string(out)) != expected {
			t.Fatal("Cloud revision mismatch")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	// Cleanup runs LIFO: let the peer drain stdin/close cleanly before cancelling
	// its process context. A function defer would kill it before t.Cleanup runs.
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "node", filepath.Join("testdata", "publication-control-cloud.mjs"))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case err := <-done:
			if err != nil {
				t.Error("Cloud fixture failed", err)
			}
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("Cloud fixture did not stop")
		}
	})
	scanner := bufio.NewScanner(stdout)
	read := func(target any) {
		t.Helper()
		if !scanner.Scan() {
			t.Fatal("Cloud response missing", scanner.Err())
		}
		if err := json.Unmarshal(scanner.Bytes(), target); err != nil {
			t.Fatal(err)
		}
	}
	command := func(v any, target any) {
		t.Helper()
		if err := json.NewEncoder(stdin).Encode(v); err != nil {
			t.Fatal(err)
		}
		read(target)
	}
	var peer struct {
		Issuer, Audience, CA, SourceHash, RequestHash string
		RequestBytes                                  int64
	}
	read(&peer)
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("cats_cancel_contract_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`)
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema)
	u.RawQuery = query.Encode()
	scopedDSN := u.String()
	a := &postgres.Adapter{}
	if err := a.Open(scopedDSN); err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	scoped, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	actor, err := a.CreateUser(&types.User{Username: "cancel-owner", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := a.CreateUser(&types.User{Username: "cancel-bot", AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveBotConfigWithOwner(bot, actor, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Exec(`UPDATE bot_config SET body_id='cancel-body' WHERE user_id=$1`, bot); err != nil {
		t.Fatal(err)
	}
	generation, err := a.BumpBotConnectionGeneration(bot)
	if err != nil {
		t.Fatal(err)
	}
	source := store.SkillHubPublicationSource{DeviceID: "cancel-device", BodyID: "cancel-body", InstallationID: "cancel-install", RouteGeneration: int64(generation), WorkspaceGeneration: "rev-1", LocalSkillID: "contract"}
	entry, _, err := a.CreateSkillHubPublicationEntry(ctx, store.SkillHubPublicationEntryInput{ActorUID: actor, BotUID: bot, Entrypoint: "workspace", RequestID: "cancel-contract", Source: source}, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		t.Fatal(err)
	}
	binding := store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: bot, Source: source, NodeID: "contract-node", ConnectionID: "contract-connection"}
	probe, err := a.ClaimSkillHubPublicationDelivery(ctx, entry.ID, binding)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.FinishSkillHubPublicationDelivery(ctx, *probe, ""); err != nil {
		t.Fatal(err)
	}
	grantInput := store.SkillHubPublicationUploadInput{EntryID: entry.ID, Binding: binding, Issuer: peer.Issuer, Audience: peer.Audience, Action: store.SkillHubPublicationUploadAction, Revision: 1, RequestHash: peer.RequestHash, SourceSnapshotHash: peer.SourceHash, RequestBytes: peer.RequestBytes}
	// Explicit trusted-runtime test adapter, not a live capture dispatch.
	if _, err := a.RecordSkillHubPublicationCapture(ctx, store.SkillHubPublicationCaptureInput{EntryID: entry.ID, Binding: binding, Revision: 1, RequestHash: peer.RequestHash, SourceSnapshotHash: peer.SourceHash, RequestBytes: peer.RequestBytes}); err != nil {
		t.Fatal(err)
	}
	grant, _, err := a.IssueSkillHubPublicationUploadGrant(ctx, grantInput)
	if err != nil {
		t.Fatal(err)
	}
	var wire publicationCloudReceiptRequest
	command(map[string]any{"action": "accept", "grant": map[string]any{"actor": strconv.FormatInt(actor, 10), "bot": strconv.FormatInt(bot, 10), "entryRequestId": entry.ID, "delegationId": grant.ID, "idempotencyKey": grant.IdempotencyKey}}, &wire)
	intent, err := a.RequestSkillHubPublicationCancellation(ctx, actor, entry.ID)
	if err != nil || intent.State != "waiting_receipt" {
		t.Fatal("cancellation before delayed receipt", err)
	}
	cfg := PublicationCancellationConfig{Enabled: true, Scope: store.SkillHubPublicationCancellationScope{Issuer: peer.Issuer, Audience: peer.Audience}, PeerSecret: strings.Repeat("a", 64)}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(peer.CA)) {
		t.Fatal("test CA")
	}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	w, err := newPublicationCancellationDispatcher(cfg, a, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	} // no receipt, no network
	receipt := store.SkillHubPublicationCloudReceipt{Query: grant.Input.Query(1), DelegationID: wire.DelegationID, IdempotencyKey: wire.IdempotencyKey, TaskID: wire.TaskID, EventID: wire.EventID}
	if _, err := a.RecordSkillHubPublicationCloudReceipt(ctx, receipt); err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	} // Cloud commits, response intentionally lost
	r, err := a.GetSkillHubPublicationCancellationDelivery(ctx, actor, entry.ID)
	if err != nil || r.State != "pending" || r.CancelledAt != nil {
		t.Fatalf("lost response guessed success: %+v %v", r, err)
	}
	var first struct {
		State       string
		Cleared     bool
		CancelledAt time.Time `json:"delegated_cancelled_at"`
		Count       int
	}
	command(map[string]string{"action": "state"}, &first)
	if first.State != "cancelled" || !first.Cleared || first.Count != 1 {
		t.Fatalf("Cloud did not commit %+v", first)
	}
	if _, err := scoped.Exec(`UPDATE skillhub_publication_cancel_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, entry.ID); err != nil {
		t.Fatal(err)
	}
	reopened := &postgres.Adapter{}
	if err := reopened.Open(scopedDSN); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	w, err = newPublicationCancellationDispatcher(cfg, reopened, transport)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	r, err = reopened.GetSkillHubPublicationCancellationDelivery(ctx, actor, entry.ID)
	if err != nil || r.State != "confirmed" || r.Attempts != 2 || r.CancelledAt == nil || !r.CancelledAt.Equal(first.CancelledAt) {
		t.Fatalf("replay not confirmed %+v %v", r, err)
	}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var last struct{ Count int }
	command(map[string]string{"action": "state"}, &last)
	if last.Count != 2 {
		t.Fatal("confirmed job resent")
	}
	t.Log("real two-store recovery: delayed receipt, committed cancellation/lost reply, reopened sender, same remote timestamp, no resend")
}
