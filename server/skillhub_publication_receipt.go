package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/openchat/openchat/server/store"
)

type publicationCloudReceiptRequest struct {
	SchemaVersion      int    `json:"schemaVersion"`
	EntryRequestID     string `json:"entryRequestId"`
	CandidateRevision  int    `json:"candidateRevision"`
	AcceptanceEpoch    int    `json:"acceptanceEpoch"`
	RequestHash        string `json:"requestHash"`
	SourceSnapshotHash string `json:"sourceSnapshotHash"`
	RequestBytes       int64  `json:"requestBytes"`
	DelegationID       string `json:"delegationId"`
	IdempotencyKey     string `json:"idempotencyKey"`
	TaskID             string `json:"taskId"`
	EventID            string `json:"eventId"`
}

func decodePublicationCloudReceipt(data []byte) (publicationCloudReceiptRequest, error) {
	var out publicationCloudReceiptRequest
	d := json.NewDecoder(bytes.NewReader(data))
	t, err := d.Token()
	if err != nil || t != json.Delim('{') {
		return out, store.ErrSkillHubPublicationInvalid
	}
	seen := map[string]bool{}
	for d.More() {
		key, err := d.Token()
		if err != nil {
			return out, err
		}
		k, ok := key.(string)
		if !ok || seen[k] {
			return out, store.ErrSkillHubPublicationInvalid
		}
		seen[k] = true
		switch k {
		case "schemaVersion", "entryRequestId", "candidateRevision", "acceptanceEpoch", "requestHash", "sourceSnapshotHash", "requestBytes", "delegationId", "idempotencyKey", "taskId", "eventId":
		default:
			return out, store.ErrSkillHubPublicationInvalid
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return out, store.ErrSkillHubPublicationInvalid
		}
	}
	if _, err := d.Token(); err != nil {
		return out, err
	}
	if _, err := d.Token(); err != io.EOF || len(seen) != 11 {
		return out, store.ErrSkillHubPublicationInvalid
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	if out.SchemaVersion != 1 {
		return out, store.ErrSkillHubPublicationInvalid
	}
	return out, nil
}

func (h *SkillHubPublicationPeerHandler) recordCloudReceipt(ctx context.Context, w http.ResponseWriter, data []byte, fail func(int, string)) {
	body, err := decodePublicationCloudReceipt(data)
	if err != nil {
		fail(400, "publication.invalid_request")
		return
	}
	r := store.SkillHubPublicationCloudReceipt{
		Query: store.SkillHubPublicationUploadQuery{EntryID: body.EntryRequestID, Issuer: h.issuer, Audience: h.audience,
			Action: store.SkillHubPublicationUploadAction, Revision: body.CandidateRevision, Epoch: body.AcceptanceEpoch,
			RequestHash: body.RequestHash, SourceSnapshotHash: body.SourceSnapshotHash, RequestBytes: body.RequestBytes},
		DelegationID: body.DelegationID, IdempotencyKey: body.IdempotencyKey, TaskID: body.TaskID, EventID: body.EventID,
	}
	if r.Validate() != nil {
		fail(400, "publication.invalid_request")
		return
	}
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_receipt_entry", Limit: 30, Window: time.Minute, Burst: 5}, r.Query.EntryID) {
		w.Header().Set("Retry-After", "5")
		fail(429, "publication.rate_limited")
		return
	}
	if ctx.Err() != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	result, err := h.receipts.RecordSkillHubPublicationCloudReceipt(ctx, r)
	if ctx.Err() != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationInvalid) || errors.Is(err, store.ErrSkillHubPublicationConflict) {
		fail(409, "publication.receipt_conflict")
		return
	}
	if err != nil || result == nil || result.Receipt != r {
		fail(503, "publication.peer_unavailable")
		return
	}
	writeSkillHubPublicationJSON(w, 200, map[string]any{
		"schemaVersion": 1, "recorded": true, "entryRequestId": r.Query.EntryID, "eventId": r.EventID,
		"taskId": r.TaskID, "localIntentCancelled": result.LocalIntentCancelled, "commitAllowed": false,
	})
}
