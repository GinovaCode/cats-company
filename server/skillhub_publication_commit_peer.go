package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

var publicationPermitID = regexp.MustCompile(`^pubpermit_[a-f0-9]{32}$`)

type publicationCommitPermitRequest struct {
	SchemaVersion int                                   `json:"schemaVersion"`
	Receipt       publicationCloudReceiptRequest        `json:"receipt"`
	Target        store.SkillHubPublicationCommitTarget `json:"target"`
}

// Exact keys at every level. encoding/json alone accepts duplicate keys,
// case-insensitive field matches, null primitives and invalid UTF-8.
func publicationExactObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	allowed := make(map[string]bool, len(keys))
	for _, k := range keys {
		allowed[k] = true
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	out := make(map[string]json.RawMessage, len(keys))
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return nil, store.ErrSkillHubPublicationInvalid
		}
		k, ok := token.(string)
		if !ok || !allowed[k] || out[k] != nil {
			return nil, store.ErrSkillHubPublicationInvalid
		}
		var value json.RawMessage
		if err := d.Decode(&value); err != nil || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, store.ErrSkillHubPublicationInvalid
		}
		out[k] = value
	}
	if _, err := d.Token(); err != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	if _, err := d.Token(); err != io.EOF || len(out) != len(keys) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return out, nil
}

func decodePublicationCommitPermit(data []byte) (publicationCommitPermitRequest, error) {
	var out publicationCommitPermitRequest
	object, err := publicationExactObject(data, "schemaVersion", "receipt", "target")
	if err != nil {
		return out, err
	}
	r, err := decodePublicationCloudReceipt(object["receipt"])
	if err != nil {
		return out, err
	}
	if _, err := publicationExactObject(object["target"], "candidateHash", "manifestHash", "publisherUserId", "publisherProfileId",
		"namespace", "skillId", "version", "uploadId", "releaseAt", "confirmVersionPublish"); err != nil {
		return out, err
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	if out.SchemaVersion != 1 || out.Target.Validate() != nil {
		return out, store.ErrSkillHubPublicationInvalid
	}
	out.Receipt = r
	return out, nil
}

func (b publicationCommitPermitRequest) input(issuer, audience string) store.SkillHubPublicationCommitInput {
	r := b.Receipt
	return store.SkillHubPublicationCommitInput{Target: b.Target, Receipt: store.SkillHubPublicationCloudReceipt{
		Query: store.SkillHubPublicationUploadQuery{EntryID: r.EntryRequestID, Issuer: issuer, Audience: audience,
			Action: store.SkillHubPublicationUploadAction, Revision: r.CandidateRevision, Epoch: r.AcceptanceEpoch,
			RequestHash: r.RequestHash, SourceSnapshotHash: r.SourceSnapshotHash, RequestBytes: r.RequestBytes},
		DelegationID: r.DelegationID, IdempotencyKey: r.IdempotencyKey, TaskID: r.TaskID, EventID: r.EventID}}
}

func (h *SkillHubPublicationPeerHandler) authorizeCommitPermit(ctx context.Context, w http.ResponseWriter, data []byte, fail func(int, string)) {
	body, err := decodePublicationCommitPermit(data)
	if err != nil {
		fail(400, "publication.invalid_request")
		return
	}
	in := body.input(h.issuer, h.audience)
	if in.Validate() != nil {
		fail(400, "publication.invalid_request")
		return
	}
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_commit_entry", Limit: 30, Window: time.Minute, Burst: 5}, in.Receipt.Query.EntryID) {
		w.Header().Set("Retry-After", "5")
		fail(429, "publication.rate_limited")
		return
	}
	if ctx.Err() != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	// No recording/admission fallback: the trusted human path must already
	// have persisted this exact intent. Service auth alone cannot create it.
	p, err := h.permits.AuthorizeSkillHubPublicationCommit(ctx, in)
	if ctx.Err() != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	switch {
	case errors.Is(err, store.ErrSkillHubPublicationDenied):
		fail(403, "publication.commit_denied")
		return
	case errors.Is(err, store.ErrSkillHubPublicationSettled):
		fail(409, "publication.commit_cancelled")
		return
	case errors.Is(err, store.ErrSkillHubPublicationConflict), errors.Is(err, store.ErrSkillHubPublicationInvalid):
		fail(409, "publication.commit_conflict")
		return
	}
	hash, _ := in.IdentityHash()
	if err != nil || p == nil || p.Intent.Input != in || p.Intent.IdentityHash != hash || !publicationPermitID.MatchString(p.ID) ||
		p.AuthorityEpoch < in.Receipt.Query.Epoch || p.AuthorityEpoch > store.SkillHubPublicationUploadMaxIssues ||
		p.Intent.RequestedAt.IsZero() || p.AuthorizedAt.IsZero() || p.AuthorizedAt.Before(p.Intent.RequestedAt) {
		fail(503, "publication.peer_unavailable")
		return
	}
	writeSkillHubPublicationJSON(w, 200, map[string]any{"schemaVersion": 1, "receipt": body.Receipt, "target": body.Target,
		"permitId": p.ID, "intentHash": p.Intent.IdentityHash, "authorityEpoch": p.AuthorityEpoch, "authorizedAt": p.AuthorizedAt.UTC()})
}
