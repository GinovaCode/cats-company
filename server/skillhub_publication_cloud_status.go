package server

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCloudStatusView struct {
	Stage            string     `json:"stage"`
	LocalIntentState string     `json:"localIntentState"`
	ExecutionState   string     `json:"executionState"`
	RecordedAt       *time.Time `json:"recordedAt,omitempty"`
	ObservedAt       time.Time  `json:"observedAt"`
}

func publicationCloudStatusProjection(s *store.SkillHubPublicationCloudStatus) (publicationCloudStatusView, bool) {
	if s == nil || s.ObservedAt.IsZero() || (s.LocalIntentState != "waiting_runtime" && s.LocalIntentState != "cancelled") {
		return publicationCloudStatusView{}, false
	}
	v := publicationCloudStatusView{Stage: "not_observed", LocalIntentState: s.LocalIntentState,
		ExecutionState: "unknown", ObservedAt: s.ObservedAt.UTC()}
	if s.ReceiptRecordedAt != nil {
		if s.ReceiptRecordedAt.IsZero() || s.ReceiptRecordedAt.After(s.ObservedAt) {
			return publicationCloudStatusView{}, false
		}
		v.Stage = "acceptance_recorded"
		at := s.ReceiptRecordedAt.UTC()
		v.RecordedAt = &at
	}
	return v, true
}

// Shares the authenticated read handler's rate, concurrency and time budget.
// Rechecks original publisher with the receipt's own database snapshot.
func (h *SkillHubPublicationReadHandler) readCloudStatus(w http.ResponseWriter, ctx context.Context, actor int64, id string) {
	reader, ok := h.db.(store.SkillHubPublicationCloudStatusReader)
	if !ok {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
		return
	}
	s, err := reader.GetSkillHubPublicationCloudStatus(ctx, actor, id)
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
	v, valid := publicationCloudStatusProjection(s)
	if s.EntryID != id || !valid {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.record_unavailable")
		return
	}
	writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1, "entryRequestId": id, "cloudStatus": v,
	})
}
