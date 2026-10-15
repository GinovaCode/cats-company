package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type publicationStatusTestStore struct {
	*publicationReadTestStore
	status      *store.SkillHubPublicationSourceStatus
	errStatus   error
	statusCalls atomic.Int32
	readStatus  func(context.Context, int64, string) (*store.SkillHubPublicationSourceStatus, error)
}

func (s *publicationStatusTestStore) GetSkillHubPublicationSourceStatus(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationSourceStatus, error) {
	s.statusCalls.Add(1)
	if s.readStatus != nil {
		return s.readStatus(ctx, uid, id)
	}
	return s.status, s.errStatus
}

func newPublicationStatusTestStore() *publicationStatusTestStore {
	now := time.Date(2026, 10, 15, 10, 0, 0, 0, time.UTC)
	return &publicationStatusTestStore{publicationReadTestStore: newPublicationReadTestStore(), status: &store.SkillHubPublicationSourceStatus{
		EntryID: publicationReadTestID, ActorUID: 1, State: "pending", NextAttemptAt: now, ObservedAt: now,
	}}
}

const publicationStatusTestPath = skillHubPublicationPrefix + "/entries/" + publicationReadTestID + "/source-status"

func TestSkillHubPublicationReadSourceStatusProjection(t *testing.T) {
	for _, tc := range []struct {
		state, code, stage string
		attempts           int
		expired            bool
	}{
		{"pending", "", "waiting_runtime", 0, false},
		{"pending", "publication.runtime_timeout", "retry_wait", 1, false},
		{"pending", "publication.runtime_unavailable", "retry_wait", 7, false},
		{"leased", "", "probing", 1, false},
		{"leased", "", "recovery_wait", 1, true},
		{"leased", "", "recovery_wait", 8, true},
		{"source_verified", "", "verified", 1, false},
		{"needs_user_action", "publication.source_missing", "needs_user_action", 1, false},
		{"needs_user_action", "publication.source_changed", "needs_user_action", 2, false},
		{"needs_user_action", "publication.source_unsafe", "needs_user_action", 1, false},
		{"needs_user_action", "publication.runtime_unsupported", "needs_user_action", 1, false},
		{"needs_user_action", "publication.delivery_exhausted", "needs_user_action", 8, false},
	} {
		t.Run(tc.state+tc.code+tc.stage, func(t *testing.T) {
			db := newPublicationStatusTestStore()
			s := db.status
			s.State, s.LastCode, s.Attempts = tc.state, tc.code, tc.attempts
			if tc.attempts > 0 {
				deadline := s.ObservedAt.Add(time.Second)
				s.LeaseUntil = &deadline
			}
			if tc.expired {
				deadline := s.ObservedAt
				s.LeaseUntil = &deadline
			}
			if tc.state == "source_verified" {
				verified := s.ObservedAt.Add(-time.Second)
				s.SourceVerifiedAt = &verified
			}
			rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 200)
			var got struct {
				SchemaVersion int                         `json:"schemaVersion"`
				EntryID       string                      `json:"entryRequestId"`
				Status        publicationSourceStatusView `json:"sourceStatus"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.SchemaVersion != 1 || got.EntryID != publicationReadTestID || got.Status.Stage != tc.stage || got.Status.Code != tc.code || got.Status.Attempts != tc.attempts || got.Status.MaxAttempts != 8 {
				t.Fatalf("projection: %s", rec.Body.String())
			}
			if (got.Status.RetryNotBeforeAt != nil) != (tc.stage == "retry_wait") || (got.Status.VerifiedAt != nil) != (tc.stage == "verified") {
				t.Fatalf("timestamps: %s", rec.Body.String())
			}
			for _, secret := range []string{"private-", "ActorUID", "Lease", "installation", "source_verified", "published", "node", "token"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatalf("leaked %s: %s", secret, rec.Body.String())
				}
			}
			if db.calls.Load() != 1 || db.statusCalls.Load() != 1 || db.entry.State != "waiting_runtime" || s.State != tc.state {
				t.Fatal("read mutated state or unexpected reads")
			}
		})
	}
}

func TestSkillHubPublicationReadSourceStatusFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*publicationStatusTestStore)
		code   int
	}{
		{"missing", func(s *publicationStatusTestStore) { s.status = nil }, 404},
		{"other actor", func(s *publicationStatusTestStore) { s.status.ActorUID = 2 }, 404},
		{"wrong entry", func(s *publicationStatusTestStore) { s.status.EntryID = "other" }, 503},
		{"unknown state", func(s *publicationStatusTestStore) { s.status.State = "published" }, 503},
		{"raw code", func(s *publicationStatusTestStore) { s.status.LastCode = "/root/private-secret" }, 503},
		{"missing db clock", func(s *publicationStatusTestStore) { s.status.ObservedAt = time.Time{} }, 503},
		{"missing due time", func(s *publicationStatusTestStore) { s.status.NextAttemptAt = time.Time{} }, 503},
		{"negative attempts", func(s *publicationStatusTestStore) { s.status.Attempts = -1 }, 503},
		{"extra attempts", func(s *publicationStatusTestStore) { s.status.Attempts = 9 }, 503},
		{"missing lease", func(s *publicationStatusTestStore) { s.status.Attempts = 1; s.status.State = "leased" }, 503},
		{"unattempted lease", func(s *publicationStatusTestStore) { v := s.status.ObservedAt; s.status.LeaseUntil = &v }, 503},
		{"unexpected verified", func(s *publicationStatusTestStore) { v := s.status.ObservedAt; s.status.SourceVerifiedAt = &v }, 503},
		{"db failure", func(s *publicationStatusTestStore) { s.errStatus = errors.New("/root/private-secret") }, 503},
		{"denied", func(s *publicationStatusTestStore) { s.errStatus = store.ErrSkillHubPublicationDenied }, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newPublicationStatusTestStore()
			tc.change(db)
			rec := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationStatusTestPath, 1), tc.code)
			if strings.Contains(rec.Body.String(), "private-secret") || strings.Contains(rec.Body.String(), "published") {
				t.Fatal("leaked raw state/error")
			}
		})
	}
	// Valid-looking terminal records still need matching, bounded evidence.
	for _, state := range []string{"leased", "pending", "needs_user_action", "source_verified"} {
		db := newPublicationStatusTestStore()
		s := db.status
		s.State = state
		s.Attempts = 8
		v := s.ObservedAt.Add(time.Minute)
		s.LeaseUntil = &v
		s.LastCode = "publication.runtime_timeout"
		if state == "source_verified" {
			s.LastCode = ""
			s.SourceVerifiedAt = &v
		}
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 503)
	}
}

func TestSkillHubPublicationReadSourceStatusAccessAndTransport(t *testing.T) {
	for _, tc := range []struct {
		uid  int64
		code int
	}{{0, 401}, {2, 404}, {3, 403}, {4, 403}, {5, 403}} {
		db := newPublicationStatusTestStore()
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationStatusTestPath, tc.uid), tc.code)
		if db.statusCalls.Load() != 0 {
			t.Fatal("unauthorized status read")
		}
	}
	for _, tc := range []struct {
		method, path string
		code         int
	}{
		{"POST", publicationStatusTestPath, 405},
		{"GET", publicationStatusTestPath + "?token=secret", 400},
		{"GET", publicationStatusTestPath + "/resume", 404},
		{"GET", strings.Replace(publicationStatusTestPath, "pubentry_", "%70ubentry_", 1), 404},
	} {
		db := newPublicationStatusTestStore()
		runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, tc.method, tc.path, 1), tc.code)
		if db.calls.Load() != 0 || db.statusCalls.Load() != 0 {
			t.Fatal("invalid transport reached storage")
		}
	}
	db := newPublicationStatusTestStore()
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, false), publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 404)
	if db.calls.Load() != 0 || db.statusCalls.Load() != 0 {
		t.Fatal("disabled reads reached storage")
	}
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db.publicationReadTestStore, true), publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 503)
	for _, enabled := range []bool{false, true} {
		h := NewSkillHubPublicationReadHandler(db, enabled)
		rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
		var caps map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &caps)
		if caps["sourceStatusReadsEnabled"] != enabled {
			t.Fatalf("caps: %v", caps)
		}
	}
	h := NewSkillHubPublicationReadHandler(db, true)
	// Entry and source-status share the same per-entry budget.
	for i := 0; i < 3; i++ {
		path := publicationStatusTestPath
		if i == 1 {
			path = strings.TrimSuffix(path, "/source-status")
		}
		runPublicationRead(t, h, publicationReadRequest(t, "GET", path, 1), 200)
	}
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 429)
	if rec.Header().Get("Retry-After") != "5" {
		t.Fatal("missing retry hint")
	}
	db.users[1].State = 1
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 403)
}

func TestSkillHubPublicationReadSourceStatusConcurrency(t *testing.T) {
	db := newPublicationStatusTestStore()
	for i := int64(10); i < 26; i++ {
		db.users[i] = &types.User{ID: i, AccountType: types.AccountHuman}
	}
	db.read = func(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationEntry, error) {
		e := *db.entry
		e.Input.ActorUID = uid
		return &e, nil
	}
	started := make(chan struct{}, 16)
	db.readStatus = func(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationSourceStatus, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			return nil, errors.New("missing deadline")
		}
		started <- struct{}{}
		<-ctx.Done()
		return nil, ctx.Err()
	}
	h := NewSkillHubPublicationReadHandler(db, true)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 16)
	for i := int64(10); i < 26; i++ {
		r := publicationReadRequest(t, http.MethodGet, publicationStatusTestPath, i).WithContext(ctx)
		go func() { w := httptest.NewRecorder(); h.Handle(w, r); done <- w.Code }()
	}
	for i := 0; i < 16; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("read not started")
		}
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 503)
	cancel()
	for i := 0; i < 16; i++ {
		select {
		case code := <-done:
			if code != 503 {
				t.Fatalf("cancel: %d", code)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancel not propagated")
		}
	}
	if len(h.reads) != 0 || db.statusCalls.Load() != 16 {
		t.Fatal("concurrency bound or cleanup failed")
	}
}
