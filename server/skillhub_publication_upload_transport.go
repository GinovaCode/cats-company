package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const publicationUploadRequestPrefix = "pubuploadrpc_"
const publicationUploadTimeout = 55 * time.Second

var publicationUploadKey = regexp.MustCompile(`^pubcandidate_[a-f0-9]{32}$`)

type publicationUploadPending struct {
	capture *publicationCapturePending // immutable live socket binding; no bearer
	epoch   int
	replies chan publicationCaptureReply // scalar error/identity only
	queued  bool
}

type publicationUploadTransport struct {
	h                *Hub
	captures         store.SkillHubPublicationCaptureReader
	grants           store.SkillHubPublicationUploadGrantStore
	issuer, audience string
	slots            chan struct{}
	mu               sync.Mutex
	pending          map[string]*publicationUploadPending
	active           map[string]bool
}

func newPublicationUploadTransportFromEnv(h *Hub) *publicationUploadTransport {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_UPLOAD_ENABLED"))
	if v != "1" && !strings.EqualFold(v, "true") {
		return nil
	}
	return newPublicationUploadTransport(h, strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER")),
		strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE")))
}

func newPublicationUploadTransport(h *Hub, issuer, audience string) *publicationUploadTransport {
	if h == nil || h.publicationCapture == nil || !store.ValidSkillHubPublicationOrigin(issuer) ||
		!store.ValidSkillHubPublicationOrigin(audience) || issuer == audience {
		return nil
	}
	c, ok := h.db.(store.SkillHubPublicationCaptureReader)
	g, grants := h.db.(store.SkillHubPublicationUploadGrantStore)
	if !ok || !grants {
		return nil
	}
	return &publicationUploadTransport{h: h, captures: c, grants: g, issuer: issuer, audience: audience,
		slots: make(chan struct{}, 4), pending: make(map[string]*publicationUploadPending), active: make(map[string]bool)}
}

// Internal operation, never a browser/model API. Nil means the runtime reported
// an accepted upload, NOT trusted Cloud acceptance or publication completion.
// Only the separately authenticated Cloud receipt handler can record acceptance.
// The caller must retain/reconcile work after uncertainty; no retry scheduler
// or user entry point invokes this method yet.
func (h *Hub) UploadSkillHubPublication(ctx context.Context, actor int64, entryID string) error {
	if h == nil || h.publicationUpload == nil {
		return store.ErrSkillHubPublicationDenied
	}
	return h.publicationUpload.upload(ctx, actor, entryID)
}

func (w *publicationUploadTransport) upload(parent context.Context, actor int64, id string) error {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, publicationUploadTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case w.slots <- struct{}{}:
	default:
		return store.ErrSkillHubPublicationBusy
	}
	defer func() { <-w.slots }()
	w.mu.Lock()
	if w.active[id] {
		w.mu.Unlock()
		return store.ErrSkillHubPublicationBusy
	}
	w.active[id] = true
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.active, id); w.mu.Unlock() }()
	entry, err := w.h.publicationCapture.entries.GetSkillHubPublicationEntry(ctx, actor, id)
	if err != nil {
		return err
	}
	if entry == nil || entry.ID != id || entry.Input.ActorUID != actor || entry.State != "waiting_runtime" || entry.Input.Validate() != nil {
		return store.ErrSkillHubPublicationDenied
	}
	in := entry.Input
	source, route, role, code := w.h.publicationRuntimeBinding(actor, in.BotUID, in.Source.DeviceID)
	if code != "" {
		return store.ErrSkillHubPublicationStale
	}
	source.LocalSkillID, source.WorkspaceGeneration = in.Source.LocalSkillID, in.Source.WorkspaceGeneration
	expected := in.Source
	if source.RouteGeneration < expected.RouteGeneration {
		return store.ErrSkillHubPublicationStale
	}
	expected.RouteGeneration = source.RouteGeneration
	if source != expected {
		return store.ErrSkillHubPublicationStale
	}
	deadline, _ := ctx.Deadline()
	p := &publicationUploadPending{capture: &publicationCapturePending{
		id: publicationUploadRequestPrefix + rand.Text(), entryID: id, route: route, role: role, deadline: deadline,
		binding: store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: in.BotUID, Source: source, NodeID: route.NodeID, ConnectionID: route.ConnectionID}},
		replies: make(chan publicationCaptureReply, 1)}
	if _, err = w.recheck(p); err != nil {
		return err
	}
	b := p.capture.binding
	capture, err := w.captures.ReadSkillHubPublicationCapture(ctx, id, b)
	if err != nil {
		return err
	}
	if capture == nil {
		return store.ErrSkillHubPublicationDenied
	}
	old := capture.Input.Binding
	if capture.Input.Validate() != nil || capture.Input.EntryID != id || capture.CapturedAt.IsZero() ||
		capture.Input.RequestBytes > publicationCaptureRuntimeMaxBytes || old.Source.RouteGeneration > b.Source.RouteGeneration ||
		(old.Source.RouteGeneration == b.Source.RouteGeneration && (old.NodeID != b.NodeID || old.ConnectionID != b.ConnectionID)) {
		return store.ErrSkillHubPublicationInvalid
	}
	old.Source.RouteGeneration, old.NodeID, old.ConnectionID = b.Source.RouteGeneration, b.NodeID, b.ConnectionID
	if old != b {
		return store.ErrSkillHubPublicationInvalid
	}
	if _, err = w.recheck(p); err != nil {
		return err
	}
	input := store.SkillHubPublicationUploadInput{EntryID: id, Binding: b, Issuer: w.issuer, Audience: w.audience,
		Action: store.SkillHubPublicationUploadAction, Revision: capture.Input.Revision, RequestHash: capture.Input.RequestHash,
		SourceSnapshotHash: capture.Input.SourceSnapshotHash, RequestBytes: capture.Input.RequestBytes}
	g, token, err := w.grants.IssueSkillHubPublicationUploadGrant(ctx, input)
	if err != nil {
		return err
	}
	if g == nil || g.Input != input || g.Epoch < 1 || g.Epoch > store.SkillHubPublicationUploadMaxIssues ||
		!publicationUploadKey.MatchString(g.IdempotencyKey) || !g.ExpiresAt.After(time.Now()) ||
		g.ExpiresAt.After(time.Now().Add(store.SkillHubPublicationUploadTTL)) {
		return store.ErrSkillHubPublicationInvalid
	}
	if _, err = store.ParseSkillHubPublicationUploadToken(token.Reveal()); err != nil {
		return err
	}
	p.epoch = g.Epoch
	// The secret is deliberately only in this dedicated outbound request.
	// No generic forwarding, model-visible result, Redis, log or DB payload.
	msg := &ServerMessage{ThinToolRPC: &MsgThinToolRPC{Type: thinToolRPCTypeRequest, RequestID: p.capture.id,
		TargetOwnerUserID: formatUID(actor), TargetDeviceID: source.DeviceID, DeviceID: source.DeviceID,
		ToolName: string(DeviceCapabilitySkillHubPublicationUpload), CreatedAt: unixMillis(time.Now()), ExpiresAt: unixMillis(deadline),
		Payload: map[string]interface{}{"entry_id": id, "bot_uid": strconv.FormatInt(b.BotUID, 10),
			"local_skill_id": source.LocalSkillID, "workspace_generation": source.WorkspaceGeneration,
			"grant": map[string]interface{}{
				"issuer": g.Input.Issuer, "audience": g.Input.Audience, "action": g.Input.Action,
				"entryId": id, "candidateRevision": g.Input.Revision, "authorityEpoch": g.Epoch,
				"binding": map[string]interface{}{"botUid": strconv.FormatInt(b.BotUID, 10), "ownerUid": strconv.FormatInt(actor, 10),
					"bodyId": source.BodyID, "installationId": source.InstallationID},
				"requestHash": g.Input.RequestHash, "sourceSnapshotHash": g.Input.SourceSnapshotHash, "requestBytes": g.Input.RequestBytes,
				"idempotencyKey": g.IdempotencyKey, "token": token.Reveal(), "expiresAt": g.ExpiresAt.UTC().Format("2006-01-02T15:04:05.000Z"),
			}}}}
	data, err := json.Marshal(msg)
	if err != nil {
		return store.ErrSkillHubPublicationInvalid
	}
	// Do not retain the bearer alongside the pending receipt.
	msg = nil
	w.mu.Lock()
	w.pending[p.capture.id] = p
	w.mu.Unlock()
	defer func() { w.mu.Lock(); delete(w.pending, p.capture.id); w.mu.Unlock() }()
	if err = ctx.Err(); err != nil {
		return err
	}
	target, err := w.recheck(p)
	if err != nil {
		return err
	}
	if !target.trySend(data) {
		return store.ErrSkillHubPublicationBusy
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case r := <-p.replies:
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := w.recheck(p)
		if err != nil || current != r.client {
			publicationDeliveryAck(r.client, r.id, http.StatusConflict)
			return store.ErrSkillHubPublicationStale
		}
		status := http.StatusOK
		if r.err != nil {
			status = http.StatusUnprocessableEntity
		}
		publicationDeliveryAck(r.client, r.id, status)
		return r.err
	}
}

func (w *publicationUploadTransport) recheck(p *publicationUploadPending) (*Client, error) {
	client, err := w.h.publicationCapture.recheck(p.capture)
	if err != nil {
		return nil, err
	}
	b := p.capture.binding
	d, ok := w.h.userDevices.activeDevice(b.ActorUID, b.Source.DeviceID)
	if ok {
		for _, capability := range d.Capabilities {
			if capability == DeviceCapabilitySkillHubPublicationUpload {
				return client, nil
			}
		}
	}
	return nil, store.ErrSkillHubPublicationDenied
}

// No DB work and no raw runtime payload on the WebSocket read loop.
func (w *publicationUploadTransport) acceptResult(client *Client, msg *MsgThinToolRPC) {
	w.mu.Lock()
	p := w.pending[msg.RequestID]
	if p == nil || !time.Now().Before(p.capture.deadline) {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusNotFound)
		return
	}
	b := p.capture.binding
	w.h.mu.RLock()
	matched := client != nil && client.accountType == types.AccountBot && client.uid == b.BotUID && client.deviceConnector == nil && client.wikiAgentUID == 0 &&
		w.h.clientsByConn[b.ConnectionID] == client && client.connectionID == b.ConnectionID && w.h.nodeID == b.NodeID &&
		client.deviceOwnerUID == b.ActorUID && client.deviceID == b.Source.DeviceID && client.bodyID == b.Source.BodyID && client.installationID == b.Source.InstallationID
	w.h.mu.RUnlock()
	if !matched {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusForbidden)
		return
	}
	if p.queued {
		w.mu.Unlock()
		publicationDeliveryAck(client, msg.ID, http.StatusConflict)
		return
	}
	r := publicationCaptureReply{client: client, id: msg.ID, err: store.ErrSkillHubPublicationInvalid}
	bound := msg.Type == thinToolRPCTypeResult && msg.ToolName == string(DeviceCapabilitySkillHubPublicationUpload) &&
		msg.DeviceID == b.Source.DeviceID && msg.TargetDeviceID == b.Source.DeviceID && msg.TargetOwnerUserID == formatUID(b.ActorUID) && len(msg.Payload) == 0
	if result, ok := msg.Result.(map[string]interface{}); bound && ok && msg.Error == nil && len(result) == 4 &&
		result["schemaVersion"] == float64(1) && result["entryId"] == p.capture.entryID &&
		result["authorityEpoch"] == float64(p.epoch) && result["accepted"] == true {
		r.err = nil
	} else if bound && msg.Result == nil && msg.Error != nil {
		switch msg.Error.Code {
		case "publication.upload_busy", "publication.upload_timeout", "publication.upload_unavailable", "publication.upload_inactive",
			"BOT_NOT_ACTIVE", "REQUEST_EXPIRED":
			r.err = store.ErrSkillHubPublicationBusy
		}
	}
	p.queued = true
	p.replies <- r
	w.mu.Unlock()
}
