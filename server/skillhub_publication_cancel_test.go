package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

const publicationCancelTestPath = skillHubPublicationPrefix + "/entries/" + publicationReadTestID + "/cancel"

type publicationCancelTestStore struct {
	*publicationStatusTestStore
	cancelCalls atomic.Int32
	cancel      func(context.Context, int64, string) (*store.SkillHubPublicationEntry, error)
}

func (s *publicationCancelTestStore) CancelSkillHubPublicationEntry(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationEntry, error) {
	s.cancelCalls.Add(1)
	if s.cancel != nil {
		return s.cancel(ctx, uid, id)
	}
	if uid != s.entry.Input.ActorUID || id != s.entry.ID {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if _, ok := ctx.Deadline(); !ok {
		return nil, errors.New("missing deadline")
	}
	s.entry.State = "cancelled"
	s.status.State = "cancelled"
	e := *s.entry
	return &e, nil
}

func newPublicationCancelHandler() (*publicationCancelTestStore, *SkillHubPublicationReadHandler) {
	s := &publicationCancelTestStore{publicationStatusTestStore: newPublicationStatusTestStore()}
	h := NewSkillHubPublicationReadHandler(s, true)
	h.cancellationEnabled = true
	return s, h
}

func TestSkillHubPublicationReadCancellationAndStatus(t *testing.T) {
	s, h := newPublicationCancelHandler()
	before := s.entry.Input
	for i := 0; i < 2; i++ {
		rec := runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), 200)
		var got struct {
			Schema int                          `json:"schemaVersion"`
			Entry  skillHubPublicationEntryView `json:"entry"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.Schema != 1 || got.Entry.State != "cancelled" || got.Entry.EntryRequestID != publicationReadTestID {
			t.Fatalf("cancel=%s", rec.Body.String())
		}
		for _, secret := range []string{"private-", "identityHash", "source", "token", "installation"} {
			if strings.Contains(rec.Body.String(), secret) {
				t.Fatalf("leaked %s", secret)
			}
		}
	}
	if s.entry.Input != before {
		t.Fatal("cancellation changed immutable identity")
	}
	// A read does not repeat cancellation; cancellation history remains readable
	// when admission/delivery/cancellation are disabled during a rollback.
	h.cancellationEnabled = false
	runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID, 1), 200)
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 200)
	if !strings.Contains(rec.Body.String(), `"stage":"cancelled"`) || s.cancelCalls.Load() != 2 {
		t.Fatalf("history=%s calls=%d", rec.Body.String(), s.cancelCalls.Load())
	}
}

func TestSkillHubPublicationReadCancellationGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		change func(*publicationCancelTestStore, *SkillHubPublicationReadHandler, *http.Request)
	}{
		{"disabled", 404, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			h.cancellationEnabled = false
		}},
		{"reads disabled", 404, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			h.enabled = false
		}},
		{"GET cannot cancel", 405, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			r.Method = "GET"
		}},
		{"body", 400, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			r.ContentLength = 2
		}},
		{"query", 400, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			r.URL.RawQuery = "actor=1"
		}},
		{"chunked", 400, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			r.TransferEncoding = []string{"chunked"}
		}},
		{"cookie only", 401, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			r.Header.Del("Authorization")
			r.AddCookie(&http.Cookie{Name: "session", Value: "fake"})
		}},
		{"duplicate authorization", 401, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			r.Header.Add("Authorization", r.Header.Get("Authorization"))
		}},
		{"disabled human", 403, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			s.users[1].State = 1
		}},
		{"wrong publisher", 404, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			s.entry.Input.ActorUID = 2
		}},
		{"missing", 404, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) { s.entry = nil }},
		{"unknown state", 503, func(s *publicationCancelTestStore, h *SkillHubPublicationReadHandler, r *http.Request) {
			s.entry.State = "published"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, h := newPublicationCancelHandler()
			r := publicationReadRequest(t, "POST", publicationCancelTestPath, 1)
			tc.change(s, h, r)
			runPublicationRead(t, h, r, tc.status)
			if s.cancelCalls.Load() != 0 {
				t.Fatal("invalid/unauthorized transport mutated entry")
			}
		})
	}
	for _, uid := range []int64{3, 5} {
		s, h := newPublicationCancelHandler()
		runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, uid), 403)
		if s.cancelCalls.Load() != 0 {
			t.Fatal("nonhuman cancelled")
		}
	}
}

func TestSkillHubPublicationReadCancellationErrors(t *testing.T) {
	for _, tc := range []struct {
		err    error
		status int
		code   string
	}{
		{store.ErrSkillHubPublicationDenied, 404, "publication.not_found"},
		{store.ErrSkillHubPublicationInvalid, 404, "publication.not_found"},
		{store.ErrSkillHubPublicationConflict, 409, "publication.cancellation_conflict"},
		{errors.New("private secret token"), 503, "publication.cancellation_uncertain"},
	} {
		t.Run(tc.code, func(t *testing.T) {
			s, h := newPublicationCancelHandler()
			s.cancel = func(context.Context, int64, string) (*store.SkillHubPublicationEntry, error) { return nil, tc.err }
			rec := runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), tc.status)
			if !strings.Contains(rec.Body.String(), tc.code) || strings.Contains(rec.Body.String(), "secret") {
				t.Fatalf("error=%s", rec.Body.String())
			}
		})
	}
	t.Run("unsupported adapter", func(t *testing.T) {
		s := newPublicationStatusTestStore()
		h := NewSkillHubPublicationReadHandler(s, true)
		h.cancellationEnabled = true
		runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), 503)
	})
	for _, bad := range []string{"nil", "wrong identity", "not cancelled"} {
		t.Run(bad, func(t *testing.T) {
			s, h := newPublicationCancelHandler()
			s.cancel = func(context.Context, int64, string) (*store.SkillHubPublicationEntry, error) {
				if bad == "nil" {
					return nil, nil
				}
				e := *s.entry
				e.State = "cancelled"
				if bad == "wrong identity" {
					e.Input.BotUID++
				} else {
					e.State = "waiting_runtime"
				}
				return &e, nil
			}
			runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), 503)
		})
	}
}

func TestSkillHubPublicationReadCancellationLimitsAndContext(t *testing.T) {
	t.Run("rate", func(t *testing.T) {
		s, h := newPublicationCancelHandler()
		for i := 0; i < 3; i++ {
			runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), 200)
		}
		rec := runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), 429)
		if rec.Header().Get("Retry-After") == "" || s.cancelCalls.Load() != 3 {
			t.Fatal("unbounded cancellation")
		}
	})
	t.Run("concurrency", func(t *testing.T) {
		s, h := newPublicationCancelHandler()
		for i := 0; i < cap(h.reads); i++ {
			h.reads <- struct{}{}
		}
		runPublicationRead(t, h, publicationReadRequest(t, "POST", publicationCancelTestPath, 1), 503)
		if s.cancelCalls.Load() != 0 {
			t.Fatal("ignored concurrency bound")
		}
	})
	t.Run("client abort may follow commit", func(t *testing.T) {
		s, h := newPublicationCancelHandler()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		s.cancel = func(ctx context.Context, uid int64, id string) (*store.SkillHubPublicationEntry, error) {
			s.entry.State = "cancelled"
			cancel()
			return nil, ctx.Err()
		}
		r := publicationReadRequest(t, "POST", publicationCancelTestPath, 1).WithContext(ctx)
		runPublicationRead(t, h, r, 503)
		rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+publicationReadTestID, 1), 200)
		if !strings.Contains(rec.Body.String(), `"state":"cancelled"`) {
			t.Fatal("committed cancellation lost")
		}
	})
}

func TestSkillHubPublicationReadCancellationCapabilities(t *testing.T) {
	for _, read := range []string{"", "true"} {
		for _, flag := range []string{"", "false", "TRUE"} {
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", read)
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CANCELLATION_ENABLED", flag)
			s, _ := newPublicationCancelHandler()
			for _, db := range []store.Store{s, s.publicationStatusTestStore} {
				h := NewSkillHubPublicationReadHandlerFromEnv(db)
				rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
				var caps map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &caps); err != nil {
					t.Fatal(err)
				}
				if caps["entryCancellationEnabled"] != (read == "true" && flag == "TRUE" && db == s) {
					t.Fatalf("caps=%s", rec.Body.String())
				}
			}
		}
	}
}

func TestSkillHubPublicationReadCancelledSourceHidesOldOutcome(t *testing.T) {
	s, h := newPublicationCancelHandler()
	s.entry.State = "cancelled"
	s.status.State = "cancelled"
	s.status.Attempts = 1
	past, future := s.status.ObservedAt.Add(-time.Second), s.status.ObservedAt.Add(time.Second)
	s.status.SourceVerifiedAt = &past
	s.status.LeaseUntil = &future
	rec := runPublicationRead(t, h, publicationReadRequest(t, "GET", publicationStatusTestPath, 1), 200)
	if !strings.Contains(rec.Body.String(), `"stage":"cancelled"`) || strings.Contains(rec.Body.String(), "verifiedAt") || strings.Contains(rec.Body.String(), "retryNotBeforeAt") {
		t.Fatalf("outcome=%s", rec.Body.String())
	}
}
