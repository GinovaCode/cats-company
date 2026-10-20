package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCaptureStatusView struct {
	Stage            string     `json:"stage"`
	Attempts         int        `json:"attempts"`
	MaxAttempts      int        `json:"maxAttempts"`
	Code             string     `json:"code,omitempty"`
	RetryNotBeforeAt *time.Time `json:"retryNotBeforeAt,omitempty"`
	CapturedAt       *time.Time `json:"capturedAt,omitempty"`
	ObservedAt       time.Time  `json:"observedAt"`
}

func publicationCaptureStatusProjection(s *store.SkillHubPublicationCaptureStatus) (publicationCaptureStatusView, bool) {
	v := publicationCaptureStatusView{}
	if s == nil || s.ObservedAt.IsZero() || s.Attempts < 0 || s.Attempts > store.SkillHubPublicationDeliveryAttempts {
		return v, false
	}
	v = publicationCaptureStatusView{Attempts: s.Attempts, MaxAttempts: store.SkillHubPublicationDeliveryAttempts, ObservedAt: s.ObservedAt.UTC()}
	if s.State == "cancelled" {
		v.Stage = "cancelled"
		return v, true
	}
	if s.State == "captured" {
		if s.CapturedAt == nil || s.CapturedAt.IsZero() || s.CapturedAt.After(s.ObservedAt) {
			return v, false
		}
		v.Stage = "captured" // Not Cloud accepted, validated or published.
		t := s.CapturedAt.UTC()
		v.CapturedAt = &t
		return v, true
	}
	if s.CapturedAt != nil {
		return v, false
	}
	switch s.State {
	case "waiting_source", "waiting_runtime":
		if s.Attempts != 0 || s.LastCode != "" || s.LeaseUntil != nil || s.NextAttemptAt != nil {
			return v, false
		}
		v.Stage = s.State
	case "leased", "pending", "needs_attention":
		if s.Attempts == 0 || s.LeaseUntil == nil || s.LeaseUntil.IsZero() || s.NextAttemptAt == nil || s.NextAttemptAt.IsZero() {
			return v, false
		}
		switch s.State {
		case "leased":
			if s.LastCode != "" {
				return v, false
			}
			v.Stage = "capturing"
			if !s.LeaseUntil.After(s.ObservedAt) {
				v.Stage = "recovery_wait"
			}
		case "pending":
			if s.LastCode != "retry" || s.Attempts >= store.SkillHubPublicationDeliveryAttempts {
				return v, false
			}
			v.Stage, v.Code = "retry_wait", "publication.capture_retry"
			t := s.NextAttemptAt.UTC()
			v.RetryNotBeforeAt = &t
		case "needs_attention":
			if s.LastCode != "rejected" && !(s.LastCode == "exhausted" && s.Attempts == store.SkillHubPublicationDeliveryAttempts) {
				return v, false
			}
			v.Stage, v.Code = "needs_user_action", "publication.capture_"+s.LastCode
		}
	default:
		return v, false
	}
	return v, true
}

func (h *SkillHubPublicationReadHandler) readCaptureStatus(w http.ResponseWriter, ctx context.Context, actor int64, id string) {
	reader, ok := h.db.(store.SkillHubPublicationCaptureStatusReader)
	if !ok {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
		return
	}
	s, err := reader.GetSkillHubPublicationCaptureStatus(ctx, actor, id)
	if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if err != nil || ctx.Err() != nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.read_unavailable")
		return
	}
	if s == nil || s.ActorUID != actor {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	v, valid := publicationCaptureStatusProjection(s)
	if s.EntryID != id || !valid {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.record_unavailable")
		return
	}
	writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{"schemaVersion": 1, "entryRequestId": id, "captureStatus": v})
}
