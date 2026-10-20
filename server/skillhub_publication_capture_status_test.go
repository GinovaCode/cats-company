package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCaptureStatusTestStore struct {
	*publicationReadTestStore
	status *store.SkillHubPublicationCaptureStatus
	err    error
}

func (s *publicationCaptureStatusTestStore) GetSkillHubPublicationCaptureStatus(context.Context, int64, string) (*store.SkillHubPublicationCaptureStatus, error) {
	return s.status, s.err
}
func newCaptureStatusTestStore() *publicationCaptureStatusTestStore {
	return &publicationCaptureStatusTestStore{publicationReadTestStore: newPublicationReadTestStore(), status: &store.SkillHubPublicationCaptureStatus{
		EntryID: publicationReadTestID, ActorUID: 1, State: "waiting_source", ObservedAt: time.Now(),
	}}
}

const captureStatusTestPath = skillHubPublicationPrefix + "/entries/" + publicationReadTestID + "/capture-status"

func TestSkillHubPublicationReadCaptureStatus(t *testing.T) {
	for _, tc := range []struct {
		state, code, stage string
		attempts           int
	}{
		{"waiting_source", "", "waiting_source", 0}, {"waiting_runtime", "", "waiting_runtime", 0},
		{"leased", "", "capturing", 1}, {"leased", "", "recovery_wait", 8}, {"pending", "retry", "retry_wait", 2},
		{"needs_attention", "rejected", "needs_user_action", 1}, {"needs_attention", "exhausted", "needs_user_action", 8},
		{"captured", "", "captured", 0}, {"captured", "retry", "captured", 2}, {"cancelled", "retry", "cancelled", 2},
	} {
		t.Run(tc.stage+tc.code, func(t *testing.T) {
			db := newCaptureStatusTestStore()
			s := db.status
			s.State, s.LastCode, s.Attempts = tc.state, tc.code, tc.attempts
			if tc.attempts > 0 {
				lease, next := s.ObservedAt.Add(time.Second), s.ObservedAt.Add(time.Minute)
				s.LeaseUntil, s.NextAttemptAt = &lease, &next
			}
			if tc.stage == "recovery_wait" {
				s.LeaseUntil = &s.ObservedAt
			}
			if tc.state == "captured" || tc.state == "cancelled" {
				captured := s.ObservedAt.Add(-time.Second)
				s.CapturedAt = &captured
			}
			r := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", captureStatusTestPath, 1), 200)
			var got struct {
				Status publicationCaptureStatusView `json:"captureStatus"`
			}
			if err := json.Unmarshal(r.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if got.Status.Stage != tc.stage || got.Status.MaxAttempts != 8 || (got.Status.CapturedAt != nil) != (tc.stage == "captured") || (got.Status.RetryNotBeforeAt != nil) != (tc.stage == "retry_wait") {
				t.Fatal(r.Body.String())
			}
			for _, secret := range []string{"requestHash", "installation", "token", "Lease", "actor", "published", "cloudTask"} {
				if strings.Contains(r.Body.String(), secret) {
					t.Fatal("private projection", r.Body.String())
				}
			}
			if r.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("cacheable status")
			}
		})
	}
}
func TestSkillHubPublicationReadCaptureStatusFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*publicationCaptureStatusTestStore)
		code   int
	}{
		{"missing", func(s *publicationCaptureStatusTestStore) { s.status = nil }, 404},
		{"actor", func(s *publicationCaptureStatusTestStore) { s.status.ActorUID = 2 }, 404},
		{"entry", func(s *publicationCaptureStatusTestStore) { s.status.EntryID = "other" }, 503},
		{"clock", func(s *publicationCaptureStatusTestStore) { s.status.ObservedAt = time.Time{} }, 503},
		{"attempts", func(s *publicationCaptureStatusTestStore) { s.status.Attempts = 9 }, 503},
		{"raw-code", func(s *publicationCaptureStatusTestStore) { s.status.LastCode = "/private/token" }, 503},
		{"no-evidence", func(s *publicationCaptureStatusTestStore) { s.status.State = "captured" }, 503},
		{"published", func(s *publicationCaptureStatusTestStore) { s.status.State = "published" }, 503},
		{"no-lease", func(s *publicationCaptureStatusTestStore) { s.status.State = "leased"; s.status.Attempts = 1 }, 503},
		{"db-error", func(s *publicationCaptureStatusTestStore) { s.err = errors.New("/private/token") }, 503},
		{"denied", func(s *publicationCaptureStatusTestStore) { s.err = store.ErrSkillHubPublicationDenied }, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newCaptureStatusTestStore()
			tc.mutate(db)
			r := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", captureStatusTestPath, 1), tc.code)
			if strings.Contains(r.Body.String(), "/private/") {
				t.Fatal("raw error")
			}
		})
	}
}
func TestSkillHubPublicationReadCaptureStatusGateAndSharedLimits(t *testing.T) {
	db := newCaptureStatusTestStore()
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, false), publicationReadRequest(t, "GET", captureStatusTestPath, 1), 404)
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "POST", captureStatusTestPath, 1), 405)
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", captureStatusTestPath+"?secret=abc", 1), 400)
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), publicationReadRequest(t, "GET", captureStatusTestPath, 2), 404)
	req := publicationReadRequest(t, "GET", captureStatusTestPath, 1)
	req.Header.Del("Authorization")
	runPublicationRead(t, NewSkillHubPublicationReadHandler(db, true), req, 401)
	h := NewSkillHubPublicationReadHandler(db, true)
	for _, p := range []string{captureStatusTestPath, skillHubPublicationPrefix + "/entries/" + publicationReadTestID, captureStatusTestPath} {
		runPublicationRead(t, h, publicationReadRequest(t, "GET", p, 1), 200)
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", captureStatusTestPath, 1), 429)
	for _, enabled := range []bool{false, true} {
		r := runPublicationRead(t, NewSkillHubPublicationReadHandler(db, enabled), publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", 1), 200)
		var m map[string]interface{}
		_ = json.Unmarshal(r.Body.Bytes(), &m)
		if m["captureStatusReadsEnabled"] != enabled {
			t.Fatal("incorrect capability")
		}
	}
}
