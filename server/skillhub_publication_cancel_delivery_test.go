package server

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func cancellationTestLease(origin string) store.SkillHubPublicationCancellationLease {
	return store.SkillHubPublicationCancellationLease{CancellationID: "pubcancel_" + strings.Repeat("1", 32), Token: strings.Repeat("2", 32), Attempt: 1, ExpiresAt: time.Now().Add(time.Minute),
		Receipt: store.SkillHubPublicationCloudReceipt{Query: store.SkillHubPublicationUploadQuery{EntryID: "pubentry_" + strings.Repeat("3", 32), Issuer: "https://catsco.example", Audience: origin, Action: store.SkillHubPublicationUploadAction, Revision: 1, Epoch: 1, RequestHash: strings.Repeat("4", 64), SourceSnapshotHash: strings.Repeat("5", 64), RequestBytes: 123},
			DelegationID: "pubgrant_" + strings.Repeat("6", 32), IdempotencyKey: "pubcandidate_" + strings.Repeat("7", 32), TaskID: "pubtask_12345678-1234-4123-8123-123456789abc", EventID: "pubevent_12345678-1234-4123-8123-123456789abc"}}
}
func cancellationTestAck(l store.SkillHubPublicationCancellationLease) map[string]any {
	return map[string]any{"schemaVersion": 1, "entryRequestId": l.Receipt.Query.EntryID, "taskId": l.Receipt.TaskID, "candidateRevision": 1, "executionState": "cancelled", "candidateAvailable": false, "cancelledAt": "2026-10-19T00:00:00.000Z", "observedAt": "2026-10-19T00:00:01.000Z", "action": "cancel", "eventId": l.Receipt.EventID, "cancellationId": l.CancellationID}
}

type cancellationTestLedger struct {
	mu        sync.Mutex
	l         *store.SkillHubPublicationCancellationLease
	out       []store.SkillHubPublicationCancellationOutcome
	claims    int
	finishErr error
}

func (s *cancellationTestLedger) ClaimSkillHubPublicationCancellation(context.Context, store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationCancellationLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims++
	return s.l, nil
}
func (s *cancellationTestLedger) FinishSkillHubPublicationCancellation(_ context.Context, _ store.SkillHubPublicationCancellationLease, o store.SkillHubPublicationCancellationOutcome) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.out = append(s.out, o)
	return s.finishErr
}
func (s *cancellationTestLedger) GetSkillHubPublicationCancellationDelivery(context.Context, int64, string) (*store.SkillHubPublicationCancellationDelivery, error) {
	return nil, nil
}

func TestSkillHubPublicationCancellationTransport(t *testing.T) {
	for _, mode := range []string{"success", "lost_reply", "redirect", "wrong_id", "wrong_task", "wrong_event", "wrong_state", "available", "duplicate", "null", "extra", "invalid_time", "reverse_time", "not_found", "conflict", "disabled", "unauthorized", "encoding", "oversize", "cacheable"} {
		t.Run(mode, func(t *testing.T) {
			expected := cancellationTestLease("https://hub.example")
			got := make(chan publicationCloudReceiptRequest, 1)
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != publicationControlPath || r.Method != "POST" || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("Cookie") != "" {
					t.Error("transport scope")
				}
				var payload struct {
					Schema  int                            `json:"schemaVersion"`
					Action  string                         `json:"action"`
					ID      string                         `json:"cancellationId"`
					Receipt publicationCloudReceiptRequest `json:"receipt"`
				}
				if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.Schema != 1 || payload.Action != "cancel" || payload.ID != expected.CancellationID {
					t.Error("request")
				}
				got <- payload.Receipt
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				ack := cancellationTestAck(expected)
				switch mode {
				case "lost_reply":
					conn, _, _ := w.(http.Hijacker).Hijack()
					conn.Close()
					return
				case "redirect":
					w.Header().Set("Location", "https://other.example/private")
					w.WriteHeader(302)
					return
				case "wrong_id":
					ack["cancellationId"] = "pubcancel_" + strings.Repeat("0", 32)
				case "wrong_task":
					ack["taskId"] = "pubtask_00000000-0000-4000-8000-000000000000"
				case "wrong_event":
					ack["eventId"] = "pubevent_00000000-0000-4000-8000-000000000000"
				case "wrong_state":
					ack["executionState"] = "delegated_pending"
				case "available":
					ack["candidateAvailable"] = true
				case "duplicate":
					b, _ := json.Marshal(ack)
					fmt.Fprint(w, strings.Replace(string(b), `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1))
					return
				case "null":
					ack["candidateAvailable"] = nil
				case "extra":
					ack["commitAllowed"] = true
				case "invalid_time":
					ack["cancelledAt"] = "yesterday"
				case "reverse_time":
					ack["observedAt"] = "2026-10-18T00:00:00.000Z"
				case "not_found", "conflict", "disabled", "unauthorized":
					status := 404
					if mode == "conflict" {
						status = 409
					}
					if mode == "unauthorized" {
						status = 401
					}
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"code": "skillhub.publication_control_" + mode, "message": "safe"}})
					return
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "oversize":
					fmt.Fprint(w, strings.Repeat("x", 4097))
					return
				case "cacheable":
					w.Header().Set("Cache-Control", "public")
				}
				json.NewEncoder(w).Encode(ack)
			}))
			defer peer.Close()
			lease := cancellationTestLease(peer.URL)
			ledger := &cancellationTestLedger{l: &lease}
			cfg := PublicationCancellationConfig{Enabled: true, Scope: store.SkillHubPublicationCancellationScope{Issuer: lease.Receipt.Query.Issuer, Audience: peer.URL}, PeerSecret: strings.Repeat("a", 64)}
			worker, err := newPublicationCancellationDispatcher(cfg, ledger, peer.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			if err := worker.RunOnce(context.Background()); err != nil {
				t.Fatal(err)
			}
			want := "retry"
			if mode == "success" {
				want = "confirmed"
			}
			if mode == "not_found" || mode == "conflict" {
				want = mode
			}
			if len(ledger.out) != 1 || ledger.out[0].Kind != want {
				t.Fatalf("outcome %+v", ledger.out)
			}
			if <-got != publicationControlReceipt(lease.Receipt) {
				t.Fatal("receipt changed")
			}
		})
	}
}

func TestSkillHubPublicationCancellationDispatcherOffAndScope(t *testing.T) {
	ledger := &cancellationTestLedger{}
	off, err := NewPublicationCancellationDispatcher(PublicationCancellationConfig{}, ledger)
	if err != nil {
		t.Fatal(err)
	}
	if err := off.RunOnce(context.Background()); err != nil || ledger.claims != 0 {
		t.Fatal("default enabled")
	}
	if _, err := NewPublicationCancellationDispatcher(PublicationCancellationConfig{Enabled: true}, ledger); err == nil {
		t.Fatal("invalid config")
	}
	lease := cancellationTestLease("https://hub.example")
	cfg := PublicationCancellationConfig{Enabled: true, Scope: store.SkillHubPublicationCancellationScope{Issuer: lease.Receipt.Query.Issuer, Audience: lease.Receipt.Query.Audience}, PeerSecret: strings.Repeat("a", 64)}
	w, err := NewPublicationCancellationDispatcher(cfg, ledger)
	if err != nil {
		t.Fatal(err)
	}
	transport := w.client.Transport.(*http.Transport)
	if transport.Proxy != nil || !transport.DisableCompression || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("unsafe transport")
	}
	lease.Receipt.Query.Audience = "https://other.example"
	ledger.l = &lease
	if err := w.RunOnce(context.Background()); err == nil || len(ledger.out) != 0 {
		t.Fatal("scope mismatch accepted")
	}
}

func TestSkillHubPublicationCancellationDispatcherCancelledTransportSettlesRetry(t *testing.T) {
	started := make(chan struct{})
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(started)
		<-r.Context().Done()
	}))
	defer peer.Close()
	l := cancellationTestLease(peer.URL)
	ledger := &cancellationTestLedger{l: &l}
	cfg := PublicationCancellationConfig{Enabled: true, Scope: store.SkillHubPublicationCancellationScope{Issuer: l.Receipt.Query.Issuer, Audience: peer.URL}, PeerSecret: strings.Repeat("a", 64)}
	w, err := newPublicationCancellationDispatcher(cfg, ledger, peer.Client().Transport)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- w.RunOnce(ctx) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(ledger.out) != 1 || ledger.out[0].Kind != "retry" {
		t.Fatal("cancellation not retained")
	}
}
