package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const SkillHubPublicationHumanCommitPrefix = "/api/skillhub/publications/commit/"

type publicationHumanCommitCloud interface {
	Plan(context.Context, store.SkillHubPublicationCloudReceipt, bool) (*store.SkillHubPublicationCommitInput, error)
	Authorize(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error)
	Read(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error)
}

type publicationHumanReportCloud interface {
	Report(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error)
}

// Human-only explicit confirmation. No registration as a tool/RPC; a model or
// Bot token can neither plan nor approve. Default off, no Worker/Runtime dispatch.
type SkillHubPublicationHumanCommitHandler struct {
	db      store.Store
	reader  store.SkillHubPublicationCommitContextReader
	intents store.SkillHubPublicationCommitStore
	cloud   publicationHumanCommitCloud
	enabled bool
	limits  *HTTPRateLimiter
	slots   chan struct{}
}

func NewSkillHubPublicationHumanCommitHandlerFromEnv(db store.Store) *SkillHubPublicationHumanCommitHandler {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED"))
	h := newPublicationHumanCommitHandler(db, nil, v == "1" || strings.EqualFold(v, "true"))
	if !h.enabled {
		return h
	}
	c, err := NewPublicationCommitCloudClient(PublicationCommitCloudConfig{Enabled: true,
		Issuer:     strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER")),
		Audience:   strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE")),
		PeerSecret: os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET")})
	if err == nil {
		h.cloud = c
	}
	return h
}

func newPublicationHumanCommitHandler(db store.Store, cloud publicationHumanCommitCloud, enabled bool) *SkillHubPublicationHumanCommitHandler {
	r, _ := db.(store.SkillHubPublicationCommitContextReader)
	i, _ := db.(store.SkillHubPublicationCommitStore)
	return &SkillHubPublicationHumanCommitHandler{db: db, reader: r, intents: i, cloud: cloud, enabled: enabled, limits: NewHTTPRateLimiter(), slots: make(chan struct{}, 4)}
}

func (h *SkillHubPublicationHumanCommitHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fail := func(s int, c string) { skillHubPublicationReadError(w, s, c) }
	if h == nil || !h.enabled {
		fail(404, "publication.not_found")
		return
	}
	if h.db == nil || h.cloud == nil || h.reader == nil || h.intents == nil {
		fail(503, "publication.unavailable")
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth[0], "Bearer ")) == "" {
		fail(401, "publication.human_login_required")
		return
	}
	claims, status, _ := ownerClaimsFromRequest(r, h.db.GetUser)
	if status != 0 {
		if status >= 500 {
			fail(503, "publication.authority_unavailable")
		} else if status == 403 {
			fail(403, "publication.access_denied")
		} else {
			fail(401, "publication.human_login_required")
		}
		return
	}
	if claims.UID <= 0 {
		fail(401, "publication.human_login_required")
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, SkillHubPublicationHumanCommitPrefix), "/")
	if !strings.HasPrefix(r.URL.Path, SkillHubPublicationHumanCommitPrefix) || r.URL.RawPath != "" || len(parts) != 3 || parts[0] != "entries" || !skillHubPublicationEntryID.MatchString(parts[1]) || (parts[2] != "plan" && parts[2] != "confirm" && parts[2] != "status" && parts[2] != "report") {
		fail(404, "publication.not_found")
		return
	}
	id, action := parts[1], parts[2]
	method := http.MethodPost
	readOnly := action == "status" || action == "report"
	if readOnly {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		fail(405, "publication.method_not_allowed")
		return
	}
	// Incidental browser cookies are ignored, never used as authentication.
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Content-Encoding") != "" {
		fail(400, "publication.invalid_request")
		return
	}
	actor := strconv.FormatInt(claims.UID, 10)
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_commit_human", Limit: 30, Window: time.Minute, Burst: 5}, actor) ||
		!h.limits.allow(HTTPRateLimitConfig{Name: "publication_commit_human_entry", Limit: 12, Window: time.Minute, Burst: 3}, actor+":"+id) {
		w.Header().Set("Retry-After", "5")
		fail(429, "publication.rate_limited")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		fail(503, "publication.busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	var confirmVersion bool
	var confirmationKey string
	if readOnly {
		if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
			fail(400, "publication.invalid_request")
			return
		}
	} else {
		media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || media != "application/json" || len(r.Header.Values("Content-Type")) != 1 || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) || r.ContentLength > 2048 {
			fail(400, "publication.invalid_request")
			return
		}
		rc := http.NewResponseController(w)
		_ = rc.SetReadDeadline(time.Now().Add(5 * time.Second))
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2048))
		_ = rc.SetReadDeadline(time.Time{})
		if err != nil {
			fail(400, "publication.invalid_request")
			return
		}
		key := "confirmationKey"
		if action == "plan" {
			key = "confirmVersionPublish"
		}
		o, err := publicationExactObject(data, "schemaVersion", key)
		var schema int
		if err != nil || json.Unmarshal(o["schemaVersion"], &schema) != nil || schema != 1 {
			fail(400, "publication.invalid_request")
			return
		}
		if action == "plan" {
			if json.Unmarshal(o[key], &confirmVersion) != nil {
				fail(400, "publication.invalid_request")
				return
			}
		} else if json.Unmarshal(o[key], &confirmationKey) != nil || !publicationPeerSecretPattern.MatchString(confirmationKey) {
			fail(400, "publication.invalid_request")
			return
		}
	}
	current := !readOnly
	local, err := h.reader.GetSkillHubPublicationCommitContext(ctx, claims.UID, id, current)
	if err != nil {
		h.failure(w, err)
		return
	}
	if local == nil || local.Entry.ID != id || local.Entry.Input.ActorUID != claims.UID {
		fail(404, "publication.not_found")
		return
	}
	if local.Receipt == nil {
		fail(409, "publication.acceptance_not_observed")
		return
	}
	receipt := *local.Receipt
	if receipt.Validate() != nil || receipt.Query.EntryID != id || ctx.Err() != nil {
		fail(503, "publication.record_unavailable")
		return
	}
	if current && local.Entry.State != "waiting_runtime" {
		fail(409, "publication.commit_cancelled")
		return
	}
	if action == "report" {
		peer, ok := h.cloud.(publicationHumanReportCloud)
		if !ok {
			fail(503, "publication.report_unavailable")
			return
		}
		out, err := peer.Report(ctx, receipt)
		if err != nil || out == nil || out.SchemaVersion != 1 || out.Receipt != publicationControlReceipt(receipt) ||
			!validPublicationValidationReport(out.Validation, receipt.Query.SourceSnapshotHash) || ctx.Err() != nil {
			fail(503, "publication.report_unavailable")
			return
		}
		if !h.recheck(ctx, claims.UID, id, false, receipt) {
			fail(503, "publication.authority_changed")
			return
		}
		writeSkillHubPublicationJSON(w, 200, map[string]any{"schemaVersion": 1, "entryRequestId": id, "validation": out.Validation})
		return
	}
	if action == "plan" {
		in, err := h.cloud.Plan(ctx, receipt, confirmVersion)
		if err != nil {
			h.failure(w, err)
			return
		}
		if in == nil || in.Validate() != nil || in.Receipt != receipt || in.Target.CandidateHash != receipt.Query.SourceSnapshotHash || in.Target.ConfirmVersionPublish != confirmVersion {
			fail(503, "publication.plan_unavailable")
			return
		}
		if !h.recheck(ctx, claims.UID, id, true, receipt) {
			fail(503, "publication.authority_changed")
			return
		}
		hash, _ := in.IdentityHash()
		writeSkillHubPublicationJSON(w, 200, map[string]any{"schemaVersion": 1, "entryRequestId": id, "plan": map[string]any{
			"skillId": in.Target.SkillID, "version": in.Target.Version, "publisherNamespace": in.Target.Namespace,
			"confirmVersionPublish": in.Target.ConfirmVersionPublish, "confirmationKey": hash}})
		return
	}
	observed, err := h.cloud.Read(ctx, receipt)
	if err != nil {
		h.failure(w, err)
		return
	}
	if !validPublicationCommitObservation(observed, receipt) || ctx.Err() != nil {
		fail(503, "publication.status_unavailable")
		return
	}
	if action == "status" {
		if !h.recheck(ctx, claims.UID, id, false, receipt) {
			fail(503, "publication.authority_changed")
			return
		}
		writeSkillHubPublicationJSON(w, 200, map[string]any{"schemaVersion": 1, "entryRequestId": id,
			"executionState": observed.State, "result": observed.Result, "errorCode": observed.ErrorCode})
		return
	}
	if observed.State == "cancelled" {
		fail(409, "publication.commit_cancelled")
		return
	}
	if observed.Target == nil {
		fail(409, "publication.plan_required")
		return
	}
	in := store.SkillHubPublicationCommitInput{Receipt: receipt, Target: *observed.Target}
	hash, err := in.IdentityHash()
	if err != nil || in.Target.CandidateHash != receipt.Query.SourceSnapshotHash {
		fail(503, "publication.plan_unavailable")
		return
	}
	if hash != confirmationKey {
		fail(409, "publication.confirmation_changed")
		return
	}
	// This exact JWT-authenticated action, not the plan bool or service secret,
	// records intent. SQL rechecks current authority/cancellation atomically.
	intent, err := h.intents.RecordSkillHubPublicationCommitIntent(ctx, claims.UID, in)
	if err != nil {
		h.failure(w, err)
		return
	}
	if intent == nil || intent.Input != in || intent.IdentityHash != hash || intent.RequestedAt.IsZero() || ctx.Err() != nil {
		fail(503, "publication.commit_uncertain")
		return
	}
	ack, err := h.cloud.Authorize(ctx, receipt)
	if err != nil {
		h.failure(w, err)
		return
	}
	if ack == nil || ack.SchemaVersion != 1 || ack.Receipt != publicationControlReceipt(receipt) || !publicationCloudCommitState(ack.State) || ack.PermitID == nil || !publicationPermitID.MatchString(*ack.PermitID) {
		fail(503, "publication.commit_uncertain")
		return
	}
	if !h.recheck(ctx, claims.UID, id, false, receipt) {
		fail(503, "publication.commit_uncertain")
		return
	}
	// Even an authorize ACK saying published is not the verified package result.
	writeSkillHubPublicationJSON(w, 202, map[string]any{"schemaVersion": 1, "entryRequestId": id, "state": "commit_accepted", "executionState": "unknown"})
}

func (h *SkillHubPublicationHumanCommitHandler) recheck(ctx context.Context, actor int64, id string, current bool, r store.SkillHubPublicationCloudReceipt) bool {
	s, e := h.reader.GetSkillHubPublicationCommitContext(ctx, actor, id, current)
	return e == nil && ctx.Err() == nil && s != nil && s.Entry.ID == id && s.Entry.Input.ActorUID == actor && s.Receipt != nil && *s.Receipt == r
}
func (h *SkillHubPublicationHumanCommitHandler) failure(w http.ResponseWriter, err error) {
	status, code := 503, "publication.commit_uncertain"
	switch {
	case errors.Is(err, store.ErrSkillHubPublicationDenied):
		status, code = 403, "publication.commit_denied"
	case errors.Is(err, store.ErrSkillHubPublicationSettled):
		status, code = 409, "publication.commit_cancelled"
	case errors.Is(err, store.ErrSkillHubPublicationConflict):
		status, code = 409, "publication.commit_conflict"
	case errors.Is(err, errPublicationCommitConfirmationRequired):
		status, code = 409, "publication.version_confirmation_required"
	case errors.Is(err, errPublicationCommitRejected):
		status, code = 422, "publication.candidate_rejected"
	case errors.Is(err, errPublicationCommitNotFound):
		status, code = 404, "publication.not_found"
	case errors.Is(err, errPublicationCommitStorageLimit):
		status, code = 429, "publication.storage_limit"
	}
	skillHubPublicationReadError(w, status, code)
}
