package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const SkillHubPublicationIntrospectionPath = "/api/skillhub/publications/internal/upload-grants/introspect"
const SkillHubPublicationCloudReceiptPath = "/api/skillhub/publications/internal/cloud-receipts"
const publicationPeerBodyLimit = 4096

var publicationPeerSecretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
var publicationGrantIDPattern = regexp.MustCompile(`^pubgrant_[a-f0-9]{32}$`)
var publicationUploadKeyPattern = regexp.MustCompile(`^pubcandidate_[a-f0-9]{32}$`)

// Service-only introspection and separately gated historical receipt ingestion.
// Neither endpoint issues grants or changes entry/task execution state.
type SkillHubPublicationPeerHandler struct {
	enabled         bool
	receiptsEnabled bool
	ready           bool
	issuer          string
	audience        string
	secretHash      [32]byte
	proxies         []netip.Prefix
	reader          store.SkillHubPublicationUploadIntrospector
	receipts        store.SkillHubPublicationCloudReceiptStore
	limits          *HTTPRateLimiter
	slots           chan struct{}
}

func NewSkillHubPublicationPeerHandlerFromEnv(db store.Store) *SkillHubPublicationPeerHandler {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED"))
	h := &SkillHubPublicationPeerHandler{enabled: v == "1" || strings.EqualFold(v, "true"), limits: NewHTTPRateLimiter(), slots: make(chan struct{}, 16)}
	v = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED"))
	h.receiptsEnabled = v == "1" || strings.EqualFold(v, "true")
	if !h.enabled && !h.receiptsEnabled {
		return h
	}
	h.issuer = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER"))
	h.audience = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE"))
	secret := os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET")
	if !store.ValidSkillHubPublicationOrigin(h.issuer) || !store.ValidSkillHubPublicationOrigin(h.audience) || h.issuer == h.audience || !publicationPeerSecretPattern.MatchString(secret) {
		return h
	}
	h.secretHash = sha256.Sum256([]byte(secret))
	// No implicit trust of X-Forwarded-Proto, loopback or global proxy settings.
	// Explicit CIDRs require a sanitizing HTTPS reverse proxy on that network.
	if list := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS")); list != "" {
		parts := strings.Split(list, ",")
		if len(parts) > 16 {
			return h
		}
		for _, part := range parts {
			p, err := netip.ParsePrefix(strings.TrimSpace(part))
			if err != nil || p.Bits() == 0 || p != p.Masked() {
				return h
			}
			h.proxies = append(h.proxies, p)
		}
	}
	h.reader, _ = db.(store.SkillHubPublicationUploadIntrospector)
	h.receipts, _ = db.(store.SkillHubPublicationCloudReceiptStore)
	h.ready = true
	return h
}

func (h *SkillHubPublicationPeerHandler) secureTransport(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	proto := r.Header.Values("X-Forwarded-Proto")
	if len(proto) != 1 || proto[0] != "https" {
		return false
	}
	for _, p := range h.proxies {
		if p.Contains(ip.Unmap()) {
			return true
		}
	}
	return false
}

type publicationPeerRequest struct {
	SchemaVersion      int    `json:"schemaVersion"`
	EntryRequestID     string `json:"entryRequestId"`
	Action             string `json:"action"`
	CandidateRevision  int    `json:"candidateRevision"`
	AuthorityEpoch     int    `json:"authorityEpoch"`
	RequestHash        string `json:"requestHash"`
	SourceSnapshotHash string `json:"sourceSnapshotHash"`
	RequestBytes       int64  `json:"requestBytes"`
}

// Reject duplicate, unknown and differently-cased keys, trailing JSON and null.
// Input is already capped; decoding values never accesses a file or executes it.
func decodePublicationPeerRequest(data []byte) (publicationPeerRequest, error) {
	var out publicationPeerRequest
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
		case "schemaVersion", "entryRequestId", "action", "candidateRevision", "authorityEpoch", "requestHash", "sourceSnapshotHash", "requestBytes":
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
	if _, err := d.Token(); err != io.EOF || len(seen) != 8 {
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

func (h *SkillHubPublicationPeerHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	fail := func(status int, code string) {
		// Do not drain an untrusted/unfinished body for HTTP/1 keep-alive.
		r.Close = true
		w.Header().Set("Connection", "close")
		skillHubPublicationReadError(w, status, code)
	}
	receipt := r.URL.Path == SkillHubPublicationCloudReceiptPath
	if h == nil || r.URL.RawPath != "" || (receipt && !h.receiptsEnabled) ||
		(!receipt && (!h.enabled || r.URL.Path != SkillHubPublicationIntrospectionPath)) {
		fail(404, "publication.not_found")
		return
	}
	if !h.ready || (receipt && h.receipts == nil) || (!receipt && h.reader == nil) {
		fail(503, "publication.peer_unavailable")
		return
	}
	if !h.secureTransport(r) {
		fail(403, "publication.secure_transport_required")
		return
	}
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") {
		fail(401, "publication.peer_auth_required")
		return
	}
	secret := strings.TrimPrefix(auth[0], "Bearer ")
	if !publicationPeerSecretPattern.MatchString(secret) {
		fail(401, "publication.peer_auth_required")
		return
	}
	hash := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(hash[:], h.secretHash[:]) != 1 {
		fail(401, "publication.peer_auth_required")
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		fail(405, "publication.method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.Header.Get("Origin") != "" || r.Header.Get("Content-Encoding") != "" || len(r.TransferEncoding) != 0 || r.ContentLength <= 0 {
		fail(400, "publication.invalid_request")
		return
	}
	if r.ContentLength > publicationPeerBodyLimit {
		fail(413, "publication.request_too_large")
		return
	}
	cts := r.Header.Values("Content-Type")
	if len(cts) != 1 {
		fail(400, "publication.invalid_request")
		return
	}
	mt, params, err := mime.ParseMediaType(cts[0])
	if err != nil || mt != "application/json" || len(params) > 1 || (len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8")) {
		fail(400, "publication.invalid_request")
		return
	}
	headers := r.Header.Values("X-SkillHub-Upload-Grant")
	var token store.SkillHubPublicationUploadToken
	if receipt {
		// A durable callback never carries a short-lived upload bearer token.
		if len(headers) != 0 {
			fail(400, "publication.invalid_request")
			return
		}
	} else {
		if len(headers) != 1 {
			fail(400, "publication.invalid_request")
			return
		}
		token, err = store.ParseSkillHubPublicationUploadToken(headers[0])
		if err != nil {
			fail(400, "publication.invalid_request")
			return
		}
	}
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_peer", Limit: 120, Window: time.Minute, Burst: 20}, "cloud") {
		w.Header().Set("Retry-After", "5")
		fail(429, "publication.rate_limited")
		return
	}
	select {
	case h.slots <- struct{}{}:
		defer func() { <-h.slots }()
	default:
		w.Header().Set("Retry-After", "5")
		fail(503, "publication.busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	controller := http.NewResponseController(w)
	if err := controller.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	defer controller.SetReadDeadline(time.Time{})
	data, err := io.ReadAll(io.LimitReader(r.Body, publicationPeerBodyLimit+1))
	if err != nil || int64(len(data)) != r.ContentLength || len(data) > publicationPeerBodyLimit {
		fail(400, "publication.invalid_request")
		return
	}
	if err := controller.SetReadDeadline(time.Time{}); err != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	if receipt {
		h.recordCloudReceipt(ctx, w, data, fail)
		return
	}
	body, err := decodePublicationPeerRequest(data)
	if err != nil {
		fail(400, "publication.invalid_request")
		return
	}
	q := store.SkillHubPublicationUploadQuery{EntryID: body.EntryRequestID, Issuer: h.issuer, Audience: h.audience, Action: body.Action,
		Revision: body.CandidateRevision, Epoch: body.AuthorityEpoch, RequestHash: body.RequestHash, SourceSnapshotHash: body.SourceSnapshotHash, RequestBytes: body.RequestBytes}
	if q.Validate() != nil {
		fail(400, "publication.invalid_request")
		return
	}
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_peer_entry", Limit: 30, Window: time.Minute, Burst: 5}, q.EntryID) {
		w.Header().Set("Retry-After", "5")
		fail(429, "publication.rate_limited")
		return
	}
	if ctx.Err() != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	observation, err := h.reader.IntrospectSkillHubPublicationUploadGrant(ctx, token, q)
	if ctx.Err() != nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationStale) || errors.Is(err, store.ErrSkillHubPublicationSettled) || errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		writeSkillHubPublicationJSON(w, 200, map[string]any{"schemaVersion": 1, "active": false})
		return
	}
	if err != nil || observation == nil || observation.Grant == nil {
		fail(503, "publication.peer_unavailable")
		return
	}
	g := observation.Grant
	if g.Input.Validate() != nil || g.Input.Query(g.Epoch) != q || !publicationGrantIDPattern.MatchString(g.ID) || !publicationUploadKeyPattern.MatchString(g.IdempotencyKey) ||
		g.IssuedAt.IsZero() || observation.ObservedAt.IsZero() || observation.ObservedAt.Before(g.IssuedAt) || !observation.ObservedAt.Before(g.ExpiresAt) ||
		g.ExpiresAt.Sub(g.IssuedAt) > store.SkillHubPublicationUploadTTL {
		fail(503, "publication.peer_unavailable")
		return
	}
	// Explicit allowlist: no source reference, file path, node/socket, secret or
	// raw database struct. Only the configured Cloud peer sees canonical UID.
	writeSkillHubPublicationJSON(w, 200, map[string]any{"schemaVersion": 1, "active": true, "grant": map[string]any{
		"delegationId": g.ID, "issuer": q.Issuer, "audience": q.Audience, "action": q.Action,
		"actorCatsCoUid": strconv.FormatInt(g.Input.Binding.ActorUID, 10), "botUid": strconv.FormatInt(g.Input.Binding.BotUID, 10),
		"entryRequestId": q.EntryID, "candidateRevision": q.Revision, "authorityEpoch": q.Epoch,
		"requestHash": q.RequestHash, "sourceSnapshotHash": q.SourceSnapshotHash, "requestBytes": q.RequestBytes,
		"idempotencyKey": g.IdempotencyKey, "expiresAt": g.ExpiresAt.UTC(), "observedAt": observation.ObservedAt.UTC(),
	}})
}
