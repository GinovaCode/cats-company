package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationPeerStore struct {
	store.Store
	calls atomic.Int32
	check func(context.Context, store.SkillHubPublicationUploadToken, store.SkillHubPublicationUploadQuery) (*store.SkillHubPublicationUploadObservation, error)
}

func (s *publicationPeerStore) IntrospectSkillHubPublicationUploadGrant(ctx context.Context, tok store.SkillHubPublicationUploadToken, q store.SkillHubPublicationUploadQuery) (*store.SkillHubPublicationUploadObservation, error) {
	s.calls.Add(1)
	return s.check(ctx, tok, q)
}

type peerRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *peerRecorder) SetReadDeadline(t time.Time) error {
	r.deadlines = append(r.deadlines, t)
	return nil
}

const peerSecret = "1111111111111111111111111111111111111111111111111111111111111111"
const peerToken = "pubupload_2222222222222222222222222222222222222222222222222222222222222222"

func peerFixture(t *testing.T) (*SkillHubPublicationPeerHandler, *publicationPeerStore, publicationPeerRequest, *store.SkillHubPublicationUploadObservation) {
	t.Helper()
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED", "true")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER", "https://cats.example")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE", "https://hub.example")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", peerSecret)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS", "")
	in := store.SkillHubPublicationUploadInput{EntryID: publicationReadTestID, Issuer: "https://cats.example", Audience: "https://hub.example",
		Action: store.SkillHubPublicationUploadAction, Revision: 1, RequestHash: strings.Repeat("a", 64), SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 123,
		Binding: store.SkillHubPublicationDeliveryBinding{ActorUID: 7, BotUID: 42, NodeID: "private-node", ConnectionID: "private-socket",
			Source: store.SkillHubPublicationSource{DeviceID: "private-device", BodyID: "private-body", InstallationID: "private-install", RouteGeneration: 1, WorkspaceGeneration: "private-workspace", LocalSkillID: "private-source"}}}
	now := time.Now().UTC()
	o := &store.SkillHubPublicationUploadObservation{Grant: &store.SkillHubPublicationUploadGrant{ID: "pubgrant_" + strings.Repeat("c", 32), Input: in, Epoch: 1,
		IdempotencyKey: "pubcandidate_" + strings.Repeat("d", 32), IssuedAt: now.Add(-time.Second), ExpiresAt: now.Add(119 * time.Second)}, ObservedAt: now}
	body := publicationPeerRequest{SchemaVersion: 1, EntryRequestID: in.EntryID, Action: in.Action, CandidateRevision: 1, AuthorityEpoch: 1, RequestHash: in.RequestHash, SourceSnapshotHash: in.SourceSnapshotHash, RequestBytes: in.RequestBytes}
	s := &publicationPeerStore{}
	s.check = func(ctx context.Context, tok store.SkillHubPublicationUploadToken, q store.SkillHubPublicationUploadQuery) (*store.SkillHubPublicationUploadObservation, error) {
		if _, ok := ctx.Deadline(); !ok {
			return nil, errors.New("missing deadline")
		}
		if tok.Reveal() != peerToken || q != in.Query(1) {
			return nil, store.ErrSkillHubPublicationDenied
		}
		return o, nil
	}
	return NewSkillHubPublicationPeerHandlerFromEnv(s), s, body, o
}
func peerRequest(t *testing.T, body publicationPeerRequest) *http.Request {
	t.Helper()
	b, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "https://cats.example"+SkillHubPublicationIntrospectionPath, bytes.NewReader(b))
	r.Header.Set("Authorization", "Bearer "+peerSecret)
	r.Header.Set("X-SkillHub-Upload-Grant", peerToken)
	r.Header.Set("Content-Type", "application/json")
	return r
}
func runPeer(t *testing.T, h *SkillHubPublicationPeerHandler, r *http.Request, want int) *peerRecorder {
	t.Helper()
	w := &peerRecorder{ResponseRecorder: httptest.NewRecorder()}
	h.Handle(w, r)
	if w.Code != want {
		t.Fatalf("status=%d want=%d body=%s", w.Code, want, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatal("response cacheable")
	}
	for _, s := range []string{peerToken, peerSecret, "private-", "secret-database"} {
		if strings.Contains(w.Body.String(), s) {
			t.Fatal("sensitive response")
		}
	}
	return w
}

func TestSkillHubPublicationPeerProjection(t *testing.T) {
	h, s, body, _ := peerFixture(t)
	w := runPeer(t, h, peerRequest(t, body), 200)
	var reply struct {
		Schema int            `json:"schemaVersion"`
		Active bool           `json:"active"`
		Grant  map[string]any `json:"grant"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Active || reply.Schema != 1 || len(reply.Grant) != 15 || reply.Grant["actorCatsCoUid"] != "7" || reply.Grant["botUid"] != "42" || s.calls.Load() != 1 {
		t.Fatalf("projection=%s", w.Body.String())
	}
	if len(w.deadlines) < 2 || w.deadlines[0].IsZero() || !w.deadlines[len(w.deadlines)-1].IsZero() {
		t.Fatal("body deadline not reset")
	}
}

func TestSkillHubPublicationPeerTransport(t *testing.T) {
	for _, tc := range []struct {
		name   string
		want   int
		change func(*http.Request)
	}{
		{"no auth", 401, func(r *http.Request) { r.Header.Del("Authorization") }},
		{"human jwt", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer user.jwt.signature") }},
		{"wrong secret", 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+strings.Repeat("f", 64)) }},
		{"duplicate auth", 401, func(r *http.Request) { r.Header.Add("Authorization", "Bearer "+peerSecret) }},
		{"cookie only", 401, func(r *http.Request) {
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: "session", Value: "fake"})
		}},
		{"bot key", 401, func(r *http.Request) { r.Header.Del("Authorization"); r.Header.Set("X-API-Key", peerSecret) }},
		{"plain HTTP", 403, func(r *http.Request) { r.TLS = nil }},
		{"spoofed proxy", 403, func(r *http.Request) { r.TLS = nil; r.Header.Set("X-Forwarded-Proto", "https") }},
		{"GET", 405, func(r *http.Request) { r.Method = "GET" }},
		{"path", 404, func(r *http.Request) { r.URL.Path += "/" }},
		{"encoded path", 404, func(r *http.Request) { r.URL.RawPath = "/%61pi" }},
		{"query", 400, func(r *http.Request) { r.URL.RawQuery = "token=secret" }},
		{"empty query", 400, func(r *http.Request) { r.URL.ForceQuery = true }},
		{"browser", 400, func(r *http.Request) { r.Header.Set("Origin", "https://cats.example") }},
		{"compression", 400, func(r *http.Request) { r.Header.Set("Content-Encoding", "gzip") }},
		{"chunked", 400, func(r *http.Request) { r.TransferEncoding = []string{"chunked"} }},
		{"unknown length", 400, func(r *http.Request) { r.ContentLength = -1 }},
		{"large body", 413, func(r *http.Request) { r.ContentLength = 4097 }},
		{"missing type", 400, func(r *http.Request) { r.Header.Del("Content-Type") }},
		{"wrong type", 400, func(r *http.Request) { r.Header.Set("Content-Type", "text/plain") }},
		{"duplicate type", 400, func(r *http.Request) { r.Header.Add("Content-Type", "application/json") }},
		{"bad charset", 400, func(r *http.Request) { r.Header.Set("Content-Type", "application/json; charset=ascii") }},
		{"missing grant", 400, func(r *http.Request) { r.Header.Del("X-SkillHub-Upload-Grant") }},
		{"bad grant", 400, func(r *http.Request) { r.Header.Set("X-SkillHub-Upload-Grant", "secret") }},
		{"duplicate grant", 400, func(r *http.Request) { r.Header.Add("X-SkillHub-Upload-Grant", peerToken) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, s, b, _ := peerFixture(t)
			r := peerRequest(t, b)
			tc.change(r)
			w := runPeer(t, h, r, tc.want)
			if s.calls.Load() != 0 {
				t.Fatal("invalid request queried store")
			}
			if tc.want == 405 && w.Header().Get("Allow") != "POST" {
				t.Fatal("missing Allow")
			}
		})
	}
}

func TestSkillHubPublicationPeerStrictBody(t *testing.T) {
	for _, mode := range []string{"duplicate", "unknown", "identity", "case", "null", "array", "trailing", "missing", "bad action", "bad epoch", "bad hash", "bad size", "bad schema", "length"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b, _ := peerFixture(t)
			switch mode {
			case "bad action":
				b.Action = "publication.commit"
			case "bad epoch":
				b.AuthorityEpoch = 0
			case "bad hash":
				b.RequestHash = "x"
			case "bad size":
				b.RequestBytes = 0
			case "bad schema":
				b.SchemaVersion = 2
			}
			r := peerRequest(t, b)
			data, _ := io.ReadAll(r.Body)
			raw := string(data)
			switch mode {
			case "duplicate":
				raw = strings.TrimSuffix(raw, "}") + `,"action":"candidate.upload"}`
			case "unknown":
				raw = strings.TrimSuffix(raw, "}") + `,"issuer":"https://evil.example"}`
			case "identity":
				raw = strings.TrimSuffix(raw, "}") + `,"actorCatsCoUid":"7"}`
			case "case":
				raw = strings.Replace(raw, "schemaVersion", "SchemaVersion", 1)
			case "null":
				raw = strings.Replace(raw, `"schemaVersion":1`, `"schemaVersion":null`, 1)
			case "array":
				raw = "[" + raw + "]"
			case "trailing":
				raw += "{}"
			case "missing":
				raw = strings.Replace(raw, `"schemaVersion":1,`, "", 1)
			}
			r.Body = io.NopCloser(strings.NewReader(raw))
			r.ContentLength = int64(len(raw))
			if mode == "length" {
				r.ContentLength++
			}
			runPeer(t, h, r, 400)
			if s.calls.Load() != 0 {
				t.Fatal("bad JSON queried store")
			}
		})
	}
}

func TestSkillHubPublicationPeerConfigAndProxy(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		want       int
	}{
		{"CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED", "", 404},
		{"CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED", "yes", 404},
		{"CATSCO_SKILLHUB_PUBLICATION_ISSUER", "http://cats.example", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE", "https://cats.example", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", "", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", "legacy-secret", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS", "0.0.0.0/0", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS", "127.0.0.1/8", 503},
		{"CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS", "invalid", 503},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			_, s, b, _ := peerFixture(t)
			t.Setenv(tc.key, tc.value)
			runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(s), peerRequest(t, b), tc.want)
			if s.calls.Load() != 0 {
				t.Fatal("bad config queried store")
			}
		})
	}
	_, s, b, _ := peerFixture(t)
	runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(nil), peerRequest(t, b), 503)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS", "127.0.0.1/32,::1/128")
	for _, tc := range []struct {
		addr, proto string
		want        int
	}{{"127.0.0.1:4000", "https", 200}, {"[::1]:4000", "https", 200}, {"192.0.2.1:4000", "https", 403}, {"127.0.0.1:4000", "http", 403}, {"127.0.0.1:4000", "https,http", 403}} {
		r := peerRequest(t, b)
		r.TLS = nil
		r.RemoteAddr = tc.addr
		r.Header.Set("X-Forwarded-Proto", tc.proto)
		runPeer(t, NewSkillHubPublicationPeerHandlerFromEnv(s), r, tc.want)
	}
}

func TestSkillHubPublicationPeerInactiveAndFailure(t *testing.T) {
	for _, err := range []error{store.ErrSkillHubPublicationDenied, store.ErrSkillHubPublicationStale, store.ErrSkillHubPublicationSettled, store.ErrSkillHubPublicationInvalid, errors.New("secret-database")} {
		h, s, b, _ := peerFixture(t)
		s.check = func(context.Context, store.SkillHubPublicationUploadToken, store.SkillHubPublicationUploadQuery) (*store.SkillHubPublicationUploadObservation, error) {
			return nil, err
		}
		want := 200
		if err.Error() == "secret-database" {
			want = 503
		}
		w := runPeer(t, h, peerRequest(t, b), want)
		if want == 200 && strings.TrimSpace(w.Body.String()) != `{"active":false,"schemaVersion":1}` {
			t.Fatal("inactive response disclosed details")
		}
	}
	for _, mode := range []string{"nil", "wrong uid", "wrong scope", "bad id", "bad key", "expired", "future", "long ttl", "cancelled context"} {
		t.Run(mode, func(t *testing.T) {
			h, s, b, o := peerFixture(t)
			r := peerRequest(t, b)
			switch mode {
			case "nil":
				s.check = func(context.Context, store.SkillHubPublicationUploadToken, store.SkillHubPublicationUploadQuery) (*store.SkillHubPublicationUploadObservation, error) {
					return nil, nil
				}
			case "wrong uid":
				o.Grant.Input.Binding.ActorUID = 0
			case "wrong scope":
				o.Grant.Input.Audience = "https://other.example"
			case "bad id":
				o.Grant.ID = "private-source"
			case "bad key":
				o.Grant.IdempotencyKey = "private-source"
			case "expired":
				o.Grant.ExpiresAt = o.ObservedAt
			case "future":
				o.Grant.IssuedAt = o.ObservedAt.Add(time.Second)
			case "long ttl":
				o.Grant.ExpiresAt = o.ObservedAt.Add(time.Hour)
			case "cancelled context":
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			runPeer(t, h, r, 503)
		})
	}
}

func TestSkillHubPublicationPeerLimitsAndTLS(t *testing.T) {
	t.Run("per entry", func(t *testing.T) {
		h, s, b, _ := peerFixture(t)
		for i := 0; i < 5; i++ {
			runPeer(t, h, peerRequest(t, b), 200)
		}
		w := runPeer(t, h, peerRequest(t, b), 429)
		if s.calls.Load() != 5 || w.Header().Get("Retry-After") == "" {
			t.Fatal("rate bypass")
		}
	})
	t.Run("slots", func(t *testing.T) {
		h, s, b, _ := peerFixture(t)
		for i := 0; i < cap(h.slots); i++ {
			h.slots <- struct{}{}
		}
		runPeer(t, h, peerRequest(t, b), 503)
		if s.calls.Load() != 0 {
			t.Fatal("concurrency bypass")
		}
	})
	t.Run("deadline unsupported", func(t *testing.T) {
		h, s, b, _ := peerFixture(t)
		w := httptest.NewRecorder()
		h.Handle(w, peerRequest(t, b))
		if w.Code != 503 || s.calls.Load() != 0 {
			t.Fatal("unbounded body reader accepted")
		}
	})
	t.Run("real TLS server", func(t *testing.T) {
		h, s, b, _ := peerFixture(t)
		mux := http.NewServeMux()
		mux.HandleFunc(SkillHubPublicationIntrospectionPath, h.Handle)
		ts := httptest.NewTLSServer(mux)
		defer ts.Close()
		data, _ := json.Marshal(b)
		r, _ := http.NewRequest("POST", ts.URL+SkillHubPublicationIntrospectionPath, bytes.NewReader(data))
		r.Header.Set("Authorization", "Bearer "+peerSecret)
		r.Header.Set("X-SkillHub-Upload-Grant", peerToken)
		r.Header.Set("Content-Type", "application/json")
		response, err := ts.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 || s.calls.Load() != 1 {
			t.Fatalf("TLS response %d", response.StatusCode)
		}
	})
}

func TestSkillHubPublicationPeerSlowBodyDeadline(t *testing.T) {
	h, s, _, _ := peerFixture(t)
	ts := httptest.NewTLSServer(http.HandlerFunc(h.Handle))
	defer ts.Close()
	clientTLS := ts.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: time.Second}, "tcp", strings.TrimPrefix(ts.URL, "https://"), clientTLS)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(8 * time.Second)); err != nil {
		t.Fatal(err)
	}
	_, err = fmt.Fprintf(conn, "POST %s HTTP/1.1\r\nHost: cats.example\r\nAuthorization: Bearer %s\r\nX-SkillHub-Upload-Grant: %s\r\nContent-Type: application/json\r\nContent-Length: 100\r\n\r\n{", SkillHubPublicationIntrospectionPath, peerSecret, peerToken)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 400 || s.calls.Load() != 0 || len(h.slots) != 0 {
		t.Fatal("slow body escaped deadline or reached store")
	}
}

func TestSkillHubPublicationPeerConcurrentBound(t *testing.T) {
	h, s, b, o := peerFixture(t)
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	results := make(chan int, 16)
	s.check = func(ctx context.Context, token store.SkillHubPublicationUploadToken, q store.SkillHubPublicationUploadQuery) (*store.SkillHubPublicationUploadObservation, error) {
		entered <- struct{}{}
		<-release
		g := *o.Grant
		g.Input.EntryID = q.EntryID
		return &store.SkillHubPublicationUploadObservation{Grant: &g, ObservedAt: o.ObservedAt}, nil
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	for i := 0; i < 16; i++ {
		body := b
		body.EntryRequestID = fmt.Sprintf("pubentry_%032x", i)
		r := peerRequest(t, body)
		go func() {
			w := &peerRecorder{ResponseRecorder: httptest.NewRecorder()}
			h.Handle(w, r)
			results <- w.Code
		}()
	}
	for i := 0; i < 16; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("requests did not enter store")
		}
	}
	runPeer(t, h, peerRequest(t, b), 503)
	if s.calls.Load() != 16 {
		t.Fatal("store concurrency exceeded")
	}
	close(release)
	for i := 0; i < 16; i++ {
		select {
		case code := <-results:
			if code != 200 {
				t.Fatalf("result=%d", code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("request did not finish")
		}
	}
	if len(h.slots) != 0 {
		t.Fatal("slots not released")
	}
}
