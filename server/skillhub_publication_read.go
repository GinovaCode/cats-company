package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const skillHubPublicationPrefix = "/api/skillhub/publications"

var skillHubPublicationEntryID = regexp.MustCompile(`^pubentry_[a-f0-9]{32}$`)

// SkillHubPublicationReadHandler authenticates active humans itself, so even
// authentication errors are private/no-store and never use query credentials.
// It deliberately has no creation, runtime dispatch or Cloud transport method.
type SkillHubPublicationReadHandler struct {
	db      store.Store
	entries store.SkillHubPublicationEntryStore
	enabled bool
	limits  *HTTPRateLimiter
	reads   chan struct{}
}

func NewSkillHubPublicationReadHandler(db store.Store, enabled bool) *SkillHubPublicationReadHandler {
	entries, _ := db.(store.SkillHubPublicationEntryStore)
	return &SkillHubPublicationReadHandler{
		db: db, entries: entries, enabled: enabled,
		limits: NewHTTPRateLimiter(), reads: make(chan struct{}, 16),
	}
}

func NewSkillHubPublicationReadHandlerFromEnv(db store.Store) *SkillHubPublicationReadHandler {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED"))
	return NewSkillHubPublicationReadHandler(db, v == "1" || strings.EqualFold(v, "true"))
}

// Explicit projection: adding internal fields to the store record cannot expose
// source paths/references, request keys, identity hashes or credentials here.
type skillHubPublicationEntryView struct {
	EntryRequestID string    `json:"entryRequestId"`
	BotUID         string    `json:"botUid"`
	Entrypoint     string    `json:"entrypoint"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"createdAt"`
}

func skillHubPublicationReadError(w http.ResponseWriter, status int, code string) {
	problem := map[string]string{
		"code": code, "message": "SkillHub publication status could not be read.",
	}
	if status == http.StatusServiceUnavailable {
		// Never log raw database errors, bearer tokens or source information.
		problem["correlationId"] = rand.Text()
		log.Printf("[skillhub_publication_read] code=%s correlation_id=%s", code, problem["correlationId"])
	}
	writeSkillHubPublicationJSON(w, status, map[string]any{"error": problem})
}

func writeSkillHubPublicationJSON(w http.ResponseWriter, status int, body any) {
	// The shared writeJSON resets Cache-Control; keep this private API's policy
	// explicit without changing legacy handlers.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *SkillHubPublicationReadHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h == nil || h.db == nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.unavailable")
		return
	}
	// No cookies, query tokens, API keys or caller-provided context identities.
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth[0], "Bearer ")) == "" {
		skillHubPublicationReadError(w, http.StatusUnauthorized, "publication.human_login_required")
		return
	}
	claims, status, _ := ownerClaimsFromRequest(r, h.db.GetUser)
	if status != 0 {
		code := "publication.human_login_required"
		if status >= 500 {
			status, code = http.StatusServiceUnavailable, "publication.authority_unavailable"
		} else if status == http.StatusForbidden {
			code = "publication.access_denied"
		}
		skillHubPublicationReadError(w, status, code)
		return
	}
	if claims.UID <= 0 {
		skillHubPublicationReadError(w, http.StatusUnauthorized, "publication.human_login_required")
		return
	}
	path := r.URL.Path
	capabilities := path == skillHubPublicationPrefix+"/capabilities"
	id := strings.TrimPrefix(path, skillHubPublicationPrefix+"/entries/")
	if r.URL.RawPath != "" || (!capabilities && (!strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") || !skillHubPublicationEntryID.MatchString(id))) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		skillHubPublicationReadError(w, http.StatusMethodNotAllowed, "publication.method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		skillHubPublicationReadError(w, http.StatusBadRequest, "publication.invalid_request")
		return
	}
	actor := strconv.FormatInt(claims.UID, 10)
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_reads_actor", Limit: 60, Window: time.Minute, Burst: 10}, actor) {
		w.Header().Set("Retry-After", "5")
		skillHubPublicationReadError(w, http.StatusTooManyRequests, "publication.rate_limited")
		return
	}
	if capabilities {
		writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
			"schemaVersion": 1, "entryReadsEnabled": h.enabled && h.entries != nil,
			"workspaceCreateEnabled": false, "chatCreateEnabled": false,
			"minPollIntervalMs": 5000,
		})
		return
	}
	if !h.enabled {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if h.entries == nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
		return
	}
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_reads_entry", Limit: 12, Window: time.Minute, Burst: 3}, actor+":"+id) {
		w.Header().Set("Retry-After", "5")
		skillHubPublicationReadError(w, http.StatusTooManyRequests, "publication.rate_limited")
		return
	}
	select {
	case h.reads <- struct{}{}:
		defer func() { <-h.reads }()
	default:
		w.Header().Set("Retry-After", "5")
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	entry, err := h.entries.GetSkillHubPublicationEntry(ctx, claims.UID, id)
	if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if err != nil || ctx.Err() != nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.read_unavailable")
		return
	}
	if entry == nil || entry.Input.ActorUID != claims.UID {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	// Unknown future states must be deliberately mapped, never passed through
	// as arbitrary strings or mistaken for a successful Cloud publication.
	if entry.ID != id || entry.State != "waiting_runtime" || entry.Input.Entrypoint != "workspace" || entry.Input.BotUID <= 0 || entry.CreatedAt.IsZero() {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.record_unavailable")
		return
	}
	writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1,
		"entry": skillHubPublicationEntryView{
			EntryRequestID: entry.ID, BotUID: strconv.FormatInt(entry.Input.BotUID, 10),
			Entrypoint: "workspace", State: "waiting_runtime", CreatedAt: entry.CreatedAt.UTC(),
		},
	})
}
