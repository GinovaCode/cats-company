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
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCommitPeerStore struct {
	store.Store
	store.SkillHubPublicationCommitStore
	calls     atomic.Int32
	authorize func(context.Context, store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error)
}

func (s *publicationCommitPeerStore) AuthorizeSkillHubPublicationCommit(ctx context.Context, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error) {
	s.calls.Add(1)
	return s.authorize(ctx, in)
}
func commitPeerFixture(t *testing.T) (*SkillHubPublicationPeerHandler, *publicationCommitPeerStore, publicationCommitPermitRequest) {
	t.Helper()
	_, _, r := receiptPeerFixture(t)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_COMMIT_PERMITS_ENABLED", "true")
	b := publicationCommitPermitRequest{SchemaVersion: 1, Receipt: r, Target: store.SkillHubPublicationCommitTarget{
		CandidateHash: strings.Repeat("c", 64), ManifestHash: strings.Repeat("d", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test",
		Namespace: "publisher", SkillID: "publisher/test", Version: "1.0.1", UploadID: "upl_test", ReleaseAt: "2026-10-19T00:00:00.000Z", ConfirmVersionPublish: true}}
	s := &publicationCommitPeerStore{}
	s.authorize = func(ctx context.Context, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error) {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("missing deadline")
		}
		if in != b.input("https://cats.example", "https://hub.example") {
			t.Error("caller changed configured identity")
		}
		hash, _ := in.IdentityHash()
		at := time.Date(2026, 10, 19, 0, 0, 1, 123456000, time.UTC)
		return &store.SkillHubPublicationCommitPermit{ID: "pubpermit_" + strings.Repeat("a", 32), Intent: store.SkillHubPublicationCommitIntent{Input: in, IdentityHash: hash, RequestedAt: at.Add(-time.Second)}, AuthorityEpoch: 2, AuthorizedAt: at}, nil
	}
	return NewSkillHubPublicationPeerHandlerFromEnv(s), s, b
}
func commitPeerRequest(t *testing.T, b publicationCommitPermitRequest) *http.Request {
	t.Helper()
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://cats.example"+SkillHubPublicationCommitPermitPath, bytes.NewReader(data))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Authorization", "Bearer "+peerSecret)
	return r
}
func TestSkillHubPublicationPeerCommitProjectionAndTLS(t *testing.T) {
	h, s, b := commitPeerFixture(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(h.Handle))
	defer ts.Close()
	r := commitPeerRequest(t, b)
	r.RequestURI = ""
	r.URL.Host = strings.TrimPrefix(ts.URL, "https://")
	response, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var v map[string]any
	if err := json.NewDecoder(response.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || len(v) != 7 || v["authorityEpoch"] != float64(2) || v["authorizedAt"] != "2026-10-19T00:00:01.123456Z" || s.calls.Load() != 1 {
		t.Fatalf("projection %+v", v)
	}
	for _, host := range []string{"cats.example", "cats.example:443"} {
		r = commitPeerRequest(t, b)
		r.Host = host
		runPeer(t, h, r, 200)
	}
	for _, path := range []string{SkillHubPublicationIntrospectionPath, SkillHubPublicationCloudReceiptPath} {
		r = commitPeerRequest(t, b)
		r.URL.Path = path
		runPeer(t, h, r, 404)
	}
}
func TestSkillHubPublicationPeerCommitStrictBody(t *testing.T) {
	for _, mode := range []string{"top_duplicate", "top_extra", "top_null", "target_duplicate", "target_case", "target_null", "target_missing", "target_extra", "receipt_duplicate", "receipt_extra", "receipt_null", "schema", "array", "trailing", "utf8", "invalid_target"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b := commitPeerFixture(t)
			r := commitPeerRequest(t, b)
			data, _ := io.ReadAll(r.Body)
			raw := string(data)
			switch mode {
			case "top_duplicate":
				raw = strings.TrimSuffix(raw, "}") + `,"schemaVersion":1}`
			case "top_extra":
				raw = strings.TrimSuffix(raw, "}") + `,"actorUid":"7"}`
			case "top_null":
				raw = strings.Replace(raw, `"target":{`, `"target":null,"ignored":{`, 1)
			case "target_duplicate":
				raw = strings.Replace(raw, `"version":"1.0.1"`, `"version":"1.0.1","version":"1.0.1"`, 1)
			case "target_case":
				raw = strings.Replace(raw, "confirmVersionPublish", "ConfirmVersionPublish", 1)
			case "target_null":
				raw = strings.Replace(raw, `"confirmVersionPublish":true`, `"confirmVersionPublish":null`, 1)
			case "target_missing":
				raw = strings.Replace(raw, `,"confirmVersionPublish":true`, "", 1)
			case "target_extra":
				raw = strings.Replace(raw, `"target":{`, `"target":{"confirmedBy":"human",`, 1)
			case "receipt_duplicate":
				raw = strings.Replace(raw, `"acceptanceEpoch":1`, `"acceptanceEpoch":1,"acceptanceEpoch":1`, 1)
			case "receipt_extra":
				raw = strings.Replace(raw, `"receipt":{`, `"receipt":{"issuer":"https://other.example",`, 1)
			case "receipt_null":
				raw = strings.Replace(raw, `"acceptanceEpoch":1`, `"acceptanceEpoch":null`, 1)
			case "schema":
				raw = strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":2`, 1)
			case "array":
				raw = "[" + raw + "]"
			case "trailing":
				raw += "{}"
			case "utf8":
				raw = strings.Replace(raw, "publisher/test", string([]byte{255}), 1)
			case "invalid_target":
				raw = strings.Replace(raw, "publisher/test", "another/test", 1)
			}
			r.Body = io.NopCloser(strings.NewReader(raw))
			r.ContentLength = int64(len(raw))
			runPeer(t, h, r, 400)
			if s.calls.Load() != 0 {
				t.Fatal("invalid body reached authorization")
			}
		})
	}
}
func TestSkillHubPublicationPeerCommitAuthAndConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		change func(*http.Request)
	}{
		{"missing", 401, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"human", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer human.jwt") }},
		{"upload_as_service", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+peerToken) }},
		{"wrong_service", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("f", 64)) }},
		{"duplicate_auth", 401, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+peerSecret) }},
		{"wrong_host", 403, func(r *http.Request) { r.Host = "other.example" }},
		{"wrong_port", 403, func(r *http.Request) { r.Host = "cats.example:444" }},
		{"http", 403, func(r *http.Request) { r.TLS = nil }},
		{"spoofed_proxy", 403, func(r *http.Request) { r.TLS = nil; r.Header.Set("X-Forwarded-Proto", "https") }},
		{"cookie", 400, func(r *http.Request) { r.Header.Set("Cookie", "session=anything") }},
		{"empty_cookie", 400, func(r *http.Request) { r.Header.Set("Cookie", "") }},
		{"empty_origin", 400, func(r *http.Request) { r.Header.Set("Origin", "") }},
		{"upload_header", 400, func(r *http.Request) { r.Header.Set("X-SkillHub-Upload-Grant", peerToken) }},
		{"query", 400, func(r *http.Request) { r.URL.RawQuery = "x=1" }},
		{"method", 405, func(r *http.Request) { r.Method = "GET" }},
		{"large", 413, func(r *http.Request) { r.ContentLength = 4097 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, b := commitPeerFixture(t)
			r := commitPeerRequest(t, b)
			tc.change(r)
			runPeer(t, h, r, tc.status)
			if s.calls.Load() != 0 {
				t.Fatal("unauthenticated call")
			}
		})
	}
	for _, tc := range []struct {
		key, value string
		status     int
	}{
		{"CATSCO_SKILLHUB_PUBLICATION_COMMIT_PERMITS_ENABLED", "", 404},
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", "", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_ISSUER", "https://cats.example:444", 403},
		{"CATSCO_SKILLHUB_PUBLICATION_ISSUER", "https://cats.example:443", 200},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, s, b := commitPeerFixture(t)
			t.Setenv(tc.key, tc.value)
			// For the equivalent configured :443 origin only the transport spelling
			// changes; the exact durable receipt scope still comes from configuration.
			if tc.status == 200 {
				s.authorize = func(ctx context.Context, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error) {
					hash, _ := in.IdentityHash()
					at := time.Now()
					return &store.SkillHubPublicationCommitPermit{ID: "pubpermit_" + strings.Repeat("a", 32), Intent: store.SkillHubPublicationCommitIntent{Input: in, IdentityHash: hash, RequestedAt: at}, AuthorityEpoch: 1, AuthorizedAt: at}, nil
				}
			}
			runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(s), commitPeerRequest(t, b), tc.status)
		})
	}
	_, _, b := commitPeerFixture(t)
	runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(nil), commitPeerRequest(t, b), 503)
}
func TestSkillHubPublicationPeerCommitFailureAndAdmission(t *testing.T) {
	for _, mode := range []string{"denied", "cancelled", "conflict", "storage", "nil", "mismatch", "hash", "id", "epoch", "timestamp", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b := commitPeerFixture(t)
			base := s.authorize
			status := 503
			s.authorize = func(ctx context.Context, in store.SkillHubPublicationCommitInput) (*store.SkillHubPublicationCommitPermit, error) {
				switch mode {
				case "denied":
					return nil, store.ErrSkillHubPublicationDenied
				case "cancelled":
					return nil, store.ErrSkillHubPublicationSettled
				case "conflict":
					return nil, store.ErrSkillHubPublicationConflict
				case "storage":
					return nil, errors.New("secret-database")
				case "nil":
					return nil, nil
				}
				p, _ := base(ctx, in)
				switch mode {
				case "mismatch":
					p.Intent.Input.Target.Version = "1.0.2"
				case "hash":
					p.Intent.IdentityHash = strings.Repeat("f", 64)
				case "id":
					p.ID = "bad"
				case "epoch":
					p.AuthorityEpoch = 0
				case "timestamp":
					p.AuthorizedAt = p.Intent.RequestedAt.Add(-time.Second)
				}
				return p, nil
			}
			if mode == "denied" {
				status = 403
			}
			if mode == "cancelled" || mode == "conflict" {
				status = 409
			}
			r := commitPeerRequest(t, b)
			if mode == "deadline" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			runPeer(t, h, r, status)
		})
	}
	t.Run("rate", func(t *testing.T) {
		h, s, b := commitPeerFixture(t)
		for i := 0; i < 5; i++ {
			runPeer(t, h, commitPeerRequest(t, b), 200)
		}
		runPeer(t, h, commitPeerRequest(t, b), 429)
		if s.calls.Load() != 5 {
			t.Fatal("rate bypass")
		}
	})
	t.Run("slots", func(t *testing.T) {
		h, s, b := commitPeerFixture(t)
		for i := 0; i < cap(h.slots); i++ {
			h.slots <- struct{}{}
		}
		runPeer(t, h, commitPeerRequest(t, b), 503)
		if s.calls.Load() != 0 {
			t.Fatal("slots bypass")
		}
	})
}
