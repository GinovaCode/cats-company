package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const publicationControlPath = "/api/skills/publication-delegations/control"

var errPublicationCancellationTransport = errors.New("publication cancellation peer unavailable")

type PublicationCancellationConfig struct {
	Enabled    bool
	Scope      store.SkillHubPublicationCancellationScope
	PeerSecret string
}

func (c PublicationCancellationConfig) valid() bool {
	return c.Scope.Validate() == nil && regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(c.PeerSecret)
}

// Explicit construction only: no environment activation, Hub registration, or
// change to the existing user cancel route. Wiring waits for isolated acceptance.
type PublicationCancellationDispatcher struct {
	config  PublicationCancellationConfig
	ledger  store.SkillHubPublicationCancellationDeliveryStore
	client  *http.Client
	active  atomic.Int32
	running atomic.Bool
}

func NewPublicationCancellationDispatcher(c PublicationCancellationConfig, ledger store.SkillHubPublicationCancellationDeliveryStore) (*PublicationCancellationDispatcher, error) {
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, MaxConnsPerHost: 4,
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, TLSHandshakeTimeout: 2 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 8192, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return newPublicationCancellationDispatcher(c, ledger, transport)
}

func newPublicationCancellationDispatcher(c PublicationCancellationConfig, ledger store.SkillHubPublicationCancellationDeliveryStore, transport http.RoundTripper) (*PublicationCancellationDispatcher, error) {
	if c.Enabled && (!c.valid() || ledger == nil || transport == nil) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return &PublicationCancellationDispatcher{config: c, ledger: ledger, client: &http.Client{Transport: transport, Timeout: 6 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// Run processes one intent every five seconds. External calls never hold a DB
// lock. run-once admission also caps test/manual callers at four concurrent jobs.
func (w *PublicationCancellationDispatcher) Run(ctx context.Context) {
	if !w.config.Enabled || !w.running.CompareAndSwap(false, true) {
		return
	}
	defer w.running.Store(false)
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		_ = w.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (w *PublicationCancellationDispatcher) RunOnce(ctx context.Context) error {
	if !w.config.Enabled || ctx.Err() != nil {
		return nil
	}
	if w.active.Add(1) > 4 {
		w.active.Add(-1)
		return store.ErrSkillHubPublicationBusy
	}
	defer w.active.Add(-1)
	l, err := w.ledger.ClaimSkillHubPublicationCancellation(ctx, w.config.Scope)
	if err != nil {
		return err
	}
	if l == nil {
		return nil
	}
	if l.Validate() != nil || l.Receipt.Query.Issuer != w.config.Scope.Issuer || l.Receipt.Query.Audience != w.config.Scope.Audience {
		return store.ErrSkillHubPublicationInvalid
	}
	outcome, err := w.deliver(ctx, *l)
	if err != nil {
		outcome = store.SkillHubPublicationCancellationOutcome{Kind: "retry"}
	}
	// A cancelled transport/parent still needs bounded retry bookkeeping. If this
	// fails or the process dies, the retained lease expires and another node retries.
	finishCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.ledger.FinishSkillHubPublicationCancellation(finishCtx, *l, outcome)
}

func publicationControlReceipt(r store.SkillHubPublicationCloudReceipt) publicationCloudReceiptRequest {
	return publicationCloudReceiptRequest{SchemaVersion: 1, EntryRequestID: r.Query.EntryID, CandidateRevision: r.Query.Revision,
		AcceptanceEpoch: r.Query.Epoch, RequestHash: r.Query.RequestHash, SourceSnapshotHash: r.Query.SourceSnapshotHash,
		RequestBytes: r.Query.RequestBytes, DelegationID: r.DelegationID, IdempotencyKey: r.IdempotencyKey, TaskID: r.TaskID, EventID: r.EventID}
}

func exactControlObject(data []byte, fields ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(data) {
		return nil, errPublicationCancellationTransport
	}
	d := json.NewDecoder(bytes.NewReader(data))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return nil, errPublicationCancellationTransport
	}
	out := map[string]json.RawMessage{}
	allowed := map[string]bool{}
	for _, s := range fields {
		allowed[s] = true
	}
	for d.More() {
		k, e := d.Token()
		key, ok := k.(string)
		if e != nil || !ok || !allowed[key] || out[key] != nil {
			return nil, errPublicationCancellationTransport
		}
		var v json.RawMessage
		if d.Decode(&v) != nil || bytes.Equal(bytes.TrimSpace(v), []byte("null")) {
			return nil, errPublicationCancellationTransport
		}
		out[key] = v
	}
	if _, err = d.Token(); err != nil {
		return nil, errPublicationCancellationTransport
	}
	if _, err = d.Token(); err != io.EOF || len(out) != len(fields) {
		return nil, errPublicationCancellationTransport
	}
	return out, nil
}

func (w *PublicationCancellationDispatcher) deliver(ctx context.Context, l store.SkillHubPublicationCancellationLease) (store.SkillHubPublicationCancellationOutcome, error) {
	bad := store.SkillHubPublicationCancellationOutcome{}
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	body, _ := json.Marshal(struct {
		SchemaVersion  int                            `json:"schemaVersion"`
		Action         string                         `json:"action"`
		Receipt        publicationCloudReceiptRequest `json:"receipt"`
		CancellationID string                         `json:"cancellationId"`
	}{1, "cancel", publicationControlReceipt(l.Receipt), l.CancellationID})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.config.Scope.Audience+publicationControlPath, bytes.NewReader(body))
	if err != nil {
		return bad, errPublicationCancellationTransport
	}
	req.Header.Set("Authorization", "Bearer "+w.config.PeerSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	res, err := w.client.Do(req)
	if err != nil {
		return bad, errPublicationCancellationTransport
	}
	defer res.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control"} {
		if len(res.Header.Values(key)) > 1 {
			return bad, errPublicationCancellationTransport
		}
	}
	media, params, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) || res.Header.Get("Content-Encoding") != "" || res.ContentLength > 4096 {
		return bad, errPublicationCancellationTransport
	}
	noStore := false
	for _, v := range strings.Split(res.Header.Get("Cache-Control"), ",") {
		if strings.EqualFold(strings.TrimSpace(v), "no-store") {
			noStore = true
		}
	}
	if !noStore {
		return bad, errPublicationCancellationTransport
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, 4097))
	if err != nil || len(data) == 0 || len(data) > 4096 {
		return bad, errPublicationCancellationTransport
	}
	if res.StatusCode == 404 || res.StatusCode == 409 {
		o, err := exactControlObject(data, "error")
		if err != nil {
			return bad, err
		}
		e, err := exactControlObject(o["error"], "code", "message")
		if err != nil {
			return bad, err
		}
		var code, message string
		if json.Unmarshal(e["code"], &code) != nil || json.Unmarshal(e["message"], &message) != nil {
			return bad, errPublicationCancellationTransport
		}
		if res.StatusCode == 404 && code == "skillhub.publication_control_not_found" {
			return store.SkillHubPublicationCancellationOutcome{Kind: "not_found"}, nil
		}
		if res.StatusCode == 409 && code == "skillhub.publication_control_conflict" {
			return store.SkillHubPublicationCancellationOutcome{Kind: "conflict"}, nil
		}
		return bad, errPublicationCancellationTransport
	}
	if res.StatusCode != 200 {
		return bad, errPublicationCancellationTransport
	}
	_, err = exactControlObject(data, "schemaVersion", "entryRequestId", "taskId", "candidateRevision", "executionState", "candidateAvailable", "cancelledAt", "observedAt", "action", "eventId", "cancellationId")
	if err != nil {
		return bad, err
	}
	var ack struct {
		SchemaVersion  int    `json:"schemaVersion"`
		EntryID        string `json:"entryRequestId"`
		TaskID         string `json:"taskId"`
		Revision       int    `json:"candidateRevision"`
		State          string `json:"executionState"`
		Available      bool   `json:"candidateAvailable"`
		CancelledAt    string `json:"cancelledAt"`
		ObservedAt     string `json:"observedAt"`
		Action         string `json:"action"`
		EventID        string `json:"eventId"`
		CancellationID string `json:"cancellationId"`
	}
	if json.Unmarshal(data, &ack) != nil || ack.SchemaVersion != 1 || ack.EntryID != l.Receipt.Query.EntryID || ack.TaskID != l.Receipt.TaskID || ack.Revision != 1 || ack.State != "cancelled" || ack.Available || ack.Action != "cancel" || ack.EventID != l.Receipt.EventID || ack.CancellationID != l.CancellationID {
		return bad, errPublicationCancellationTransport
	}
	cancelledAt, e1 := time.Parse(time.RFC3339Nano, ack.CancelledAt)
	observedAt, e2 := time.Parse(time.RFC3339Nano, ack.ObservedAt)
	out := store.SkillHubPublicationCancellationOutcome{Kind: "confirmed", CancelledAt: cancelledAt, ObservedAt: observedAt}
	if e1 != nil || e2 != nil || out.Validate() != nil {
		return bad, errPublicationCancellationTransport
	}
	return out, nil
}
