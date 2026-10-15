package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/openchat/openchat/server/store"
)

// sourceStatus is separate from entry.state and Cloud publication state. A
// successful read-only probe never means captured, validated or published.
type publicationSourceStatusView struct {
	Stage            string     `json:"stage"`
	Attempts         int        `json:"attempts"`
	MaxAttempts      int        `json:"maxAttempts"`
	Code             string     `json:"code,omitempty"`
	RetryNotBeforeAt *time.Time `json:"retryNotBeforeAt,omitempty"`
	VerifiedAt       *time.Time `json:"verifiedAt,omitempty"`
	ObservedAt       time.Time  `json:"observedAt"`
}

func publicationSourceStatusProjection(s *store.SkillHubPublicationSourceStatus) (publicationSourceStatusView, bool) {
	v := publicationSourceStatusView{}
	if s == nil || s.ObservedAt.IsZero() || s.NextAttemptAt.IsZero() || s.Attempts < 0 || s.Attempts > store.SkillHubPublicationDeliveryAttempts {
		return v, false
	}
	if (s.Attempts == 0 && s.LeaseUntil != nil) || (s.Attempts > 0 && (s.LeaseUntil == nil || s.LeaseUntil.IsZero())) {
		return v, false
	}
	if s.State != "source_verified" && s.SourceVerifiedAt != nil {
		return v, false
	}
	v.Attempts, v.MaxAttempts, v.ObservedAt = s.Attempts, store.SkillHubPublicationDeliveryAttempts, s.ObservedAt.UTC()
	switch s.State {
	case "pending":
		if s.Attempts == 0 {
			if s.LastCode != "" {
				return v, false
			}
			v.Stage = "waiting_runtime"
		} else {
			if s.Attempts >= store.SkillHubPublicationDeliveryAttempts || (s.LastCode != "publication.runtime_timeout" && s.LastCode != "publication.runtime_unavailable") {
				return v, false
			}
			v.Stage, v.Code = "retry_wait", s.LastCode
			next := s.NextAttemptAt.UTC()
			v.RetryNotBeforeAt = &next
		}
	case "leased":
		if s.Attempts == 0 || s.LastCode != "" {
			return v, false
		}
		v.Stage = "probing"
		if !s.LeaseUntil.After(s.ObservedAt) {
			// A read never settles an expired lease. Even attempt eight must be
			// durably settled by the worker before reporting needs_user_action.
			v.Stage = "recovery_wait"
		}
	case "source_verified":
		if s.Attempts == 0 || s.LastCode != "" || s.SourceVerifiedAt == nil || s.SourceVerifiedAt.IsZero() || s.SourceVerifiedAt.After(s.ObservedAt) {
			return v, false
		}
		v.Stage = "verified"
		verified := s.SourceVerifiedAt.UTC()
		v.VerifiedAt = &verified
	case "needs_user_action":
		if s.Attempts == 0 {
			return v, false
		}
		switch s.LastCode {
		case "publication.source_missing", "publication.source_changed", "publication.source_unsafe", "publication.runtime_unsupported":
		case "publication.delivery_exhausted":
			if s.Attempts != store.SkillHubPublicationDeliveryAttempts {
				return v, false
			}
		default:
			return v, false
		}
		v.Stage, v.Code = "needs_user_action", s.LastCode
	default:
		return v, false
	}
	return v, true
}

// Called only after the shared authentication, transport, rate/concurrency
// limits and original-publisher entry checks. Uses the same five-second budget.
func (h *SkillHubPublicationReadHandler) readSourceStatus(w http.ResponseWriter, ctx context.Context, actor int64, id string) {
	reader, ok := h.db.(store.SkillHubPublicationSourceStatusReader)
	if !ok {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
		return
	}
	s, err := reader.GetSkillHubPublicationSourceStatus(ctx, actor, id)
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
	v, valid := publicationSourceStatusProjection(s)
	if s.EntryID != id || !valid {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.record_unavailable")
		return
	}
	writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1, "entryRequestId": id, "sourceStatus": v,
	})
}
