package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const publicationReadTestID = "pubentry_0123456789abcdef0123456789abcdef"

type publicationReadTestStore struct {
	authStateTestStore
	entry *store.SkillHubPublicationEntry
	err   error
	read  func(context.Context, int64, string) (*store.SkillHubPublicationEntry, error)
	calls atomic.Int32
}

func (s *publicationReadTestStore) CreateSkillHubPublicationEntry(context.Context, store.SkillHubPublicationEntryInput, store.SkillHubPublicationEntryPolicy) (*store.SkillHubPublicationEntry, bool, error) {
	panic("read API must never create a publication entry")
}

func (s *publicationReadTestStore) GetSkillHubPublicationEntry(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
	s.calls.Add(1)
	if s.read != nil {
		return s.read(ctx, actor, id)
	}
	// Deliberately return even a mismatched owner: the HTTP projection must
	// defend against accidental disclosure by an adapter as well.
	return s.entry, s.err
}

func newPublicationReadTestStore() *publicationReadTestStore {
	return &publicationReadTestStore{
		authStateTestStore: authStateTestStore{users: map[int64]*types.User{
			1: {ID: 1, AccountType: types.AccountHuman},
			2: {ID: 2, AccountType: types.AccountHuman},
			3: {ID: 3, AccountType: types.AccountBot},
			4: {ID: 4, AccountType: types.AccountHuman, State: 1},
			5: {ID: 5, AccountType: types.AccountService},
		}},
		entry: &store.SkillHubPublicationEntry{
			ID: publicationReadTestID, State: "waiting_runtime",
			CreatedAt:    time.Date(2026, 10, 15, 1, 2, 3, 0, time.UTC),
			IdentityHash: "private-identity-hash",
			Input: store.SkillHubPublicationEntryInput{
				RequestID: "private-request-key", ActorUID: 1, BotUID: 99, Entrypoint: "workspace",
				Source: store.SkillHubPublicationSource{DeviceID: "private-device", BodyID: "private-body", InstallationID: "private-install", RouteGeneration: 42, WorkspaceGeneration: "private-generation", LocalSkillID: "private-source"},
			},
		},
	}
}

func publicationReadRequest(t *testing.T, method, path string, uid int64) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if uid > 0 {
		token, err := GenerateToken(uid, "same-display-name", "test@example.invalid")
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func runPublicationRead(t *testing.T, h *SkillHubPublicationReadHandler, req *http.Request, want int) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.Handle(rec, req)
	if rec.Code != want {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, want, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "private, no-store" || rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("missing private headers: %v", rec.Header())
	}
	if rec.Header().Get("Location") != "" || len(rec.Result().Cookies()) != 0 {
		t.Fatal("unexpected redirect/session side effect")
	}
	return rec
}

func TestSkillHubPublicationReadHumanAuthentication(t *testing.T) {
	path := skillHubPublicationPrefix + "/entries/" + publicationReadTestID
	for _, tc := range []struct {
		name   string
		uid    int64
		status int
		modify func(*http.Request)
	}{
		{"anonymous", 0, 401, nil},
		{"active human", 1, 200, nil},
		{"other human same name", 2, 404, nil},
		{"bot JWT", 3, 403, nil},
		{"disabled human", 4, 403, nil},
		{"service JWT", 5, 403, nil},
		{"deleted human", 6, 401, nil},
		{"invalid token", 0, 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer invalid") }},
		{"bot API key", 0, 401, func(r *http.Request) { r.Header.Set("Authorization", "ApiKey cc_3_private") }},
		{"bearer API key", 0, 401, func(r *http.Request) { r.Header.Set("Authorization", "Bearer cc_3_private") }},
		{"query credential", 1, 401, func(r *http.Request) {
			r.URL.RawQuery = "token=" + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			r.Header.Del("Authorization")
		}},
		{"cookie credential", 1, 401, func(r *http.Request) {
			r.AddCookie(&http.Cookie{Name: "catsco_session", Value: strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")})
			r.Header.Del("Authorization")
		}},
		{"duplicate authorization", 1, 401, func(r *http.Request) { r.Header.Add("Authorization", r.Header.Get("Authorization")) }},
		{"forged context", 0, 401, func(r *http.Request) { *r = *r.WithContext(context.WithValue(r.Context(), uidKey, int64(1))) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newPublicationReadTestStore()
			req := publicationReadRequest(t, http.MethodGet, path, tc.uid)
			if tc.modify != nil {
				tc.modify(req)
			}
			runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), req, tc.status)
			if tc.status != 200 && tc.status != 404 && db.calls.Load() != 0 {
				t.Fatal("unauthenticated store read")
			}
		})
	}
}

func TestSkillHubPublicationReadSafeProjection(t *testing.T) {
	db := newPublicationReadTestStore()
	rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID, 1), 200)
	want := `{"schemaVersion":1,"entry":{"entryRequestId":"` + publicationReadTestID + `","botUid":"99","entrypoint":"workspace","state":"waiting_runtime","createdAt":"2026-10-15T01:02:03Z"}}`
	var gotJSON, wantJSON any
	if err := json.Unmarshal(rec.Body.Bytes(), &gotJSON); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(want), &wantJSON); err != nil {
		t.Fatal(err)
	}
	got, _ := json.Marshal(gotJSON)
	expected, _ := json.Marshal(wantJSON)
	if string(got) != string(expected) {
		t.Fatalf("unexpected fields: %s", got)
	}
	if db.calls.Load() != 1 {
		t.Fatal("read not performed exactly once")
	}
}

func TestSkillHubPublicationReadRoutesAndDisabledMode(t *testing.T) {
	base := skillHubPublicationPrefix
	for _, tc := range []struct {
		method, path string
		status       int
	}{
		{"POST", base + "/entries", 404}, {"POST", base + "/entries/" + publicationReadTestID, 405},
		{"DELETE", base + "/entries/" + publicationReadTestID, 405}, {"HEAD", base + "/entries/" + publicationReadTestID, 405},
		{"GET", base, 404}, {"GET", base + "/entries", 404}, {"GET", base + "/entries/invalid", 404},
		{"GET", base + "/entries/" + publicationReadTestID + "/cancel", 404},
		{"GET", base + "/entries/" + strings.ToUpper(publicationReadTestID), 404},
		{"GET", base + "/entries/%70ubentry_0123456789abcdef0123456789abcdef", 404},
		{"GET", base + "/entries/" + publicationReadTestID + "?actorUid=1", 400},
		{"GET", base + "/entries/" + publicationReadTestID + "?token=secret", 400},
		{"GET", base + "/entries/" + publicationReadTestID + "?", 400},
		{"POST", base + "/capabilities", 405},
	} {
		t.Run(tc.method+tc.path, func(t *testing.T) {
			db := newPublicationReadTestStore()
			rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, tc.method, tc.path, 1), tc.status)
			if tc.status == 405 && rec.Header().Get("Allow") != "GET" {
				t.Fatal("missing Allow")
			}
			if db.calls.Load() != 0 {
				t.Fatal("invalid route queried store")
			}
		})
	}
	for _, value := range []string{"", "false", "yes", "1", "TRUE"} {
		t.Run("flag="+value, func(t *testing.T) {
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", value)
			db := newPublicationReadTestStore()
			h := NewSkillHubPublicationReadHandlerFromEnv(db)
			rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", base+"/capabilities", 1), 200)
			var capabilities map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &capabilities); err != nil {
				t.Fatal(err)
			}
			enabled := value == "1" || value == "TRUE"
			if capabilities["entryReadsEnabled"] != enabled || capabilities["workspaceCreateEnabled"] != false || capabilities["chatCreateEnabled"] != false {
				t.Fatalf("capabilities=%v", capabilities)
			}
			status := 404
			if enabled {
				status = 200
			}
			runPublicationRead(t, h, publicationReadRequest(t, "GET", base+"/entries/"+publicationReadTestID, 1), status)
			if !enabled && db.calls.Load() != 0 {
				t.Fatal("disabled handler queried entry")
			}
		})
	}
	for _, chunked := range []bool{false, true} {
		db := newPublicationReadTestStore()
		req := publicationReadRequest(t, "GET", base+"/entries/"+publicationReadTestID, 1)
		if chunked {
			req.ContentLength = -1
			req.TransferEncoding = []string{"chunked"}
		} else {
			req.ContentLength = 1
		}
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), req, 400)
		if db.calls.Load() != 0 {
			t.Fatal("body was accepted")
		}
	}
}

func TestSkillHubPublicationReadFailuresArePrivate(t *testing.T) {
	path := skillHubPublicationPrefix + "/entries/" + publicationReadTestID
	for _, tc := range []struct {
		name   string
		change func(*publicationReadTestStore)
		status int
	}{
		{"missing", func(s *publicationReadTestStore) { s.entry = nil }, 404},
		{"denied", func(s *publicationReadTestStore) { s.err = store.ErrSkillHubPublicationDenied }, 404},
		{"invalid", func(s *publicationReadTestStore) { s.err = store.ErrSkillHubPublicationInvalid }, 404},
		{"database failure", func(s *publicationReadTestStore) { s.err = errors.New("postgres://password@private-host /root/secret") }, 503},
		{"authority failure", func(s *publicationReadTestStore) {
			s.getUserErr = errors.New("postgres://password@private-host /root/secret")
		}, 503},
		{"unknown state", func(s *publicationReadTestStore) { s.entry.State = "published" }, 503},
		{"unknown entrypoint", func(s *publicationReadTestStore) { s.entry.Input.Entrypoint = "chat" }, 503},
		{"wrong record", func(s *publicationReadTestStore) { s.entry.ID = "wrong" }, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newPublicationReadTestStore()
			tc.change(db)
			rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", path, 1), tc.status)
			for _, secret := range []string{"postgres", "password", "private-host", "/root/secret", "private-source", "published"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("leaked %s", secret)
				}
			}
			if tc.status == 503 && !strings.Contains(rec.Body.String(), "correlationId") {
				t.Fatal("missing correlation")
			}
		})
	}
	db := newPublicationReadTestStore()
	h := NewSkillHubPublicationReadHandler(db.authStateTestStore, true) // MySQL-style unsupported adapter.
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
	if !strings.Contains(rec.Body.String(), `"entryReadsEnabled":false`) {
		t.Fatal("unsupported storage advertised reads")
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 1), 503)
}

func TestSkillHubPublicationReadAccountStateIsRechecked(t *testing.T) {
	db := newPublicationReadTestStore()
	h := NewSkillHubPublicationReadHandler(db, true)
	req := publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID, 1)
	runPublicationRead(t, h, req, 200)
	db.users[1].State = 1
	runPublicationRead(t, h, req, 403)
	delete(db.users, 1)
	runPublicationRead(t, h, req, 401)
	if db.calls.Load() != 1 {
		t.Fatal("disabled/deleted account read private state")
	}
}

func TestSkillHubPublicationReadPollingBudgets(t *testing.T) {
	db := newPublicationReadTestStore()
	h := NewSkillHubPublicationReadHandler(db, true)
	path := skillHubPublicationPrefix + "/entries/" + publicationReadTestID
	for i := 0; i < 3; i++ {
		runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 1), 200)
	}
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 1), 429)
	if rec.Header().Get("Retry-After") != "5" || db.calls.Load() != 3 {
		t.Fatal("per-entry poll limit bypassed")
	}
	// A different account has its own budget, but still cannot read this entry.
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 2), 404)
	h = NewSkillHubPublicationReadHandler(db, true)
	for i := 0; i < 10; i++ {
		runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 1), 429)
}

func TestSkillHubPublicationReadDeadlineAndConcurrency(t *testing.T) {
	db := newPublicationReadTestStore()
	started := make(chan struct{}, 16)
	release := make(chan struct{})
	defer close(release)
	db.read = func(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationEntry, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			return nil, fmt.Errorf("missing bounded deadline")
		}
		started <- struct{}{}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-release:
			return nil, nil
		}
	}
	h := NewSkillHubPublicationReadHandler(db, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan *httptest.ResponseRecorder, 16)
	for i := int64(10); i < 26; i++ {
		db.users[i] = &types.User{ID: i, AccountType: types.AccountHuman}
	}
	for i := int64(10); i < 26; i++ {
		req := publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID, i).WithContext(ctx)
		go func() { rec := httptest.NewRecorder(); h.Handle(rec, req); completed <- rec }()
	}
	for i := 0; i < 16; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("read failed to start")
		}
	}
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID, 1), 503)
	if !strings.Contains(rec.Body.String(), "publication.busy") || db.calls.Load() != 16 {
		t.Fatal("concurrent reads unbounded")
	}
	cancel()
	for i := 0; i < 16; i++ {
		select {
		case rec := <-completed:
			if rec.Code != 503 {
				t.Fatalf("cancel status=%d", rec.Code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel did not propagate")
		}
	}
	if len(h.reads) != 0 {
		t.Fatal("concurrency slot leaked")
	}
}
