package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type publicationReceiptStore struct {
	store.Store
	calls  atomic.Int32
	record func(context.Context, store.SkillHubPublicationCloudReceipt) (*store.SkillHubPublicationCloudReceiptResult, error)
}

func (s *publicationReceiptStore) RecordSkillHubPublicationCloudReceipt(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*store.SkillHubPublicationCloudReceiptResult, error) {
	s.calls.Add(1)
	return s.record(ctx, r)
}

func receiptPeerFixture(t *testing.T) (*SkillHubPublicationPeerHandler, *publicationReceiptStore, publicationCloudReceiptRequest) {
	t.Helper()
	_, _, _, o := peerFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED", "true")
	b := publicationCloudReceiptRequest{SchemaVersion: 1, EntryRequestID: o.Grant.Input.EntryID, CandidateRevision: 1, AcceptanceEpoch: 1, RequestHash: o.Grant.Input.RequestHash, SourceSnapshotHash: o.Grant.Input.SourceSnapshotHash, RequestBytes: o.Grant.Input.RequestBytes, DelegationID: o.Grant.ID, IdempotencyKey: o.Grant.IdempotencyKey, TaskID: "pubtask_11111111-1111-4111-8111-111111111111", EventID: "pubevent_22222222-2222-4222-8222-222222222222"}
	s := &publicationReceiptStore{}
	s.record = func(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*store.SkillHubPublicationCloudReceiptResult, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing deadline")
		}
		if r.Query.Issuer != "https://cats.example" || r.Query.Audience != "https://hub.example" || r.Query.Action != store.SkillHubPublicationUploadAction {
			t.Error("not configured binding")
		}
		return &store.SkillHubPublicationCloudReceiptResult{Receipt: r}, nil
	}
	return NewSkillHubPublicationPeerHandlerFromEnv(s), s, b
}

func receiptPeerRequest(t *testing.T, b publicationCloudReceiptRequest) *http.Request {
	t.Helper()
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://cats.example"+SkillHubPublicationCloudReceiptPath, bytes.NewReader(raw))
	r.Header.Set("Authorization", "Bearer "+peerSecret)
	r.Header.Set("Content-Type", "application/json")
	return r
}

func TestSkillHubPublicationPeerReceiptProjectionAndTLS(t *testing.T) {
	h, s, b := receiptPeerFixture(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(h.Handle))
	defer ts.Close()
	r := receiptPeerRequest(t, b)
	r.RequestURI = ""
	r.URL.Scheme = "https"
	r.URL.Host = strings.TrimPrefix(ts.URL, "https://")
	resp, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != 200 || resp.Header.Get("Cache-Control") != "private, no-store" || len(got) != 7 || got["recorded"] != true || got["commitAllowed"] != false || got["localIntentCancelled"] != false || got["taskId"] != b.TaskID || got["eventId"] != b.EventID || s.calls.Load() != 1 {
		t.Fatalf("unexpected acknowledgement: %+v", got)
	}
	// This callback can be enabled without enabling introspection and vice versa.
	r = receiptPeerRequest(t, b)
	r.URL.Path = SkillHubPublicationIntrospectionPath
	runPeer(t, h, r, 404)
}

func TestSkillHubPublicationPeerReceiptStrictBody(t *testing.T) {
	for _, mode := range []string{"duplicate", "unknown", "identity", "issuer", "case", "null", "array", "trailing", "missing", "schema", "epoch", "revision", "task", "event", "grant", "key", "size", "hash", "length", "token"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b := receiptPeerFixture(t)
			switch mode {
			case "schema":
				b.SchemaVersion = 2
			case "epoch":
				b.AcceptanceEpoch = 9
			case "revision":
				b.CandidateRevision = 2
			case "task":
				b.TaskID = "private-path"
			case "event":
				b.EventID = ""
			case "grant":
				b.DelegationID = "secret"
			case "key":
				b.IdempotencyKey = "secret"
			case "size":
				b.RequestBytes = 0
			case "hash":
				b.SourceSnapshotHash = "x"
			}
			r := receiptPeerRequest(t, b)
			data, _ := io.ReadAll(r.Body)
			raw := string(data)
			switch mode {
			case "duplicate":
				raw = strings.TrimSuffix(raw, "}") + `,"schemaVersion":1}`
			case "unknown":
				raw = strings.TrimSuffix(raw, "}") + `,"state":"published"}`
			case "identity":
				raw = strings.TrimSuffix(raw, "}") + `,"actorCatsCoUid":"7"}`
			case "issuer":
				raw = strings.TrimSuffix(raw, "}") + `,"issuer":"https://evil.example"}`
			case "case":
				raw = strings.Replace(raw, "taskId", "TaskId", 1)
			case "null":
				raw = strings.Replace(raw, `"acceptanceEpoch":1`, `"acceptanceEpoch":null`, 1)
			case "array":
				raw = "[" + raw + "]"
			case "trailing":
				raw += "{}"
			case "missing":
				raw = strings.Replace(raw, `"schemaVersion":1,`, "", 1)
			case "token":
				r.Header.Set("X-SkillHub-Upload-Grant", peerToken)
			}
			r.Body = io.NopCloser(strings.NewReader(raw))
			r.ContentLength = int64(len(raw))
			if mode == "length" {
				r.ContentLength++
			}
			runPeer(t, h, r, 400)
			if s.calls.Load() != 0 {
				t.Fatal("bad input reached store")
			}
		})
	}
}

func TestSkillHubPublicationPeerReceiptAuthAndConfig(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   int
		change func(*http.Request)
	}{
		{"missing", 401, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"human", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer human.jwt") }},
		{"wrong peer", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("f", 64)) }},
		{"duplicate", 401, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+peerSecret) }},
		{"plain HTTP", 403, func(r *http.Request) { r.TLS = nil }},
		{"spoofed proxy", 403, func(r *http.Request) { r.TLS = nil; r.Header.Set("X-Forwarded-Proto", "https") }},
		{"browser", 400, func(r *http.Request) { r.Header.Set("Origin", "https://cats.example") }},
		{"query", 400, func(r *http.Request) { r.URL.RawQuery = "anything=1" }},
		{"path", 404, func(r *http.Request) { r.URL.Path += "/" }},
		{"method", 405, func(r *http.Request) { r.Method = "GET" }},
		{"chunked", 400, func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
		{"large", 413, func(r *http.Request) { r.ContentLength = 4097 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, b := receiptPeerFixture(t)
			r := receiptPeerRequest(t, b)
			tc.change(r)
			runPeer(t, h, r, tc.want)
			if s.calls.Load() != 0 {
				t.Fatal("invalid transport reached store")
			}
		})
	}
	for _, tc := range []struct {
		key, value string
		want       int
	}{
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED", "", 404},
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", "", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE", "http://hub.example", 503},
	} {
		t.Run(tc.key, func(t *testing.T) {
			_, s, b := receiptPeerFixture(t)
			t.Setenv(tc.key, tc.value)
			runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(s), receiptPeerRequest(t, b), tc.want)
		})
	}
	_, _, b := receiptPeerFixture(t)
	runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(nil), receiptPeerRequest(t, b), 503)
}

func TestSkillHubPublicationPeerReceiptFailuresAndBounds(t *testing.T) {
	for _, mode := range []string{"denied", "conflict", "database", "nil", "mismatch", "cancelled", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b := receiptPeerFixture(t)
			want := 503
			s.record = func(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*store.SkillHubPublicationCloudReceiptResult, error) {
				switch mode {
				case "denied":
					return nil, store.ErrSkillHubPublicationDenied
				case "conflict":
					return nil, store.ErrSkillHubPublicationConflict
				case "database":
					return nil, errors.New("secret-database")
				case "nil":
					return nil, nil
				case "mismatch":
					r.EventID = "private-path"
				}
				return &store.SkillHubPublicationCloudReceiptResult{Receipt: r, LocalIntentCancelled: true}, nil
			}
			if mode == "denied" || mode == "conflict" {
				want = 409
			}
			if mode == "cancelled" {
				want = 200
			}
			r := receiptPeerRequest(t, b)
			if mode == "deadline" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := runPeer(t, h, r, want)
			if mode == "cancelled" && (!strings.Contains(w.Body.String(), `"localIntentCancelled":true`) || !strings.Contains(w.Body.String(), `"commitAllowed":false`)) {
				t.Fatal("cancel acknowledgement changed authority")
			}
		})
	}
	t.Run("rate", func(t *testing.T) {
		h, s, b := receiptPeerFixture(t)
		for i := 0; i < 5; i++ {
			runPeer(t, h, receiptPeerRequest(t, b), 200)
		}
		w := runPeer(t, h, receiptPeerRequest(t, b), 429)
		if s.calls.Load() != 5 || w.Header().Get("Retry-After") != "5" {
			t.Fatal("rate bypass")
		}
	})
	t.Run("slots", func(t *testing.T) {
		h, s, b := receiptPeerFixture(t)
		for i := 0; i < cap(h.slots); i++ {
			h.slots <- struct{}{}
		}
		runPeer(t, h, receiptPeerRequest(t, b), 503)
		if s.calls.Load() != 0 {
			t.Fatal("concurrency bypass")
		}
	})
}
