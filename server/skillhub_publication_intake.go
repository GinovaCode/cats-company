package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// v1 retains its probe-before-admission behavior for existing callers. v2 saves
// an unverified intent first; ONLY the durable worker probes it afterwards.
const publicationWorkspaceCreateDurable = "skillhub.publication.createEntry.v2"

type publicationIntakeRequest struct {
	requestID, device, local, workspace string
	actor, bot                          int64
	expires                             time.Time
	client                              *Client
	route                               runtimeRoute
}

func (h *Hub) publicationIntakeReply(r publicationIntakeRequest, result any, code string) {
	if r.client == nil || r.client.uid != r.actor || !h.isClientRegistered(r.client) || !h.clientRoute(r.client).matches(r.route) {
		return // A new login/socket never inherits an in-flight private reply.
	}
	msg := &MsgThinToolRPC{Type: thinToolRPCTypeResult, RequestID: r.requestID, ToolName: publicationWorkspaceCreateDurable, Result: result}
	if code != "" {
		msg.Result = nil
		msg.Error = &MsgDeviceRPCError{Code: code, Message: "Publication admission could not be confirmed. Check the original request status before retrying; no upload was started."}
	}
	data, err := json.Marshal(&ServerMessage{ThinToolRPC: msg})
	if err == nil {
		r.client.trySend(data)
	} // Backpressure drops the reply, not the connection or durable intent.
}

func (h *Hub) handlePublicationWorkspaceDurableRequest(client *Client, msg *MsgThinToolRPC) {
	r := publicationIntakeRequest{client: client, requestID: msg.RequestID, route: h.clientRoute(client)}
	if client != nil {
		r.actor = client.uid
	}
	fail := func(code string) { h.publicationIntakeReply(r, nil, code) }
	if !store.ValidSkillHubPublicationRequestID(msg.RequestID) {
		h.sendThinToolRPCAck(client, msg.ID, http.StatusBadRequest, "invalid publication request ID", nil)
		return
	}
	if !h.publicationWorkspaceEntries || h.publicationDelivery == nil {
		fail("publication.disabled")
		return
	}
	w := h.publicationDelivery
	if !w.running.Load() || w.ctx.Err() != nil || h.publicationEntryLimits == nil || h.publicationAdmissions == nil {
		fail("publication.unavailable")
		return
	}
	entries, ok := h.db.(store.SkillHubPublicationEntryStore)
	if !ok {
		fail("publication.storage_unsupported")
		return
	}
	reader, ok := h.db.(store.SkillHubPublicationEntryRequestReader)
	if !ok {
		fail("publication.storage_unsupported")
		return
	}
	if _, ok := h.db.(store.SkillHubPublicationSourceStatusReader); !ok {
		fail("publication.storage_unsupported")
		return
	}
	if client == nil || client.uid <= 0 || client.accountType != types.AccountHuman || client.deviceConnector != nil || client.wikiAgentUID != 0 || !h.isClientRegistered(client) {
		fail("publication.owner_required")
		return
	}
	if !h.publicationEntryLimits.allow(HTTPRateLimitConfig{Name: "publication_workspace", Limit: 20, Window: time.Minute, Burst: 3}, strconv.FormatInt(client.uid, 10)) {
		fail("publication.rate_limited")
		return
	}
	botText, botOK := msg.Payload["bot_uid"].(string)
	local, localOK := msg.Payload["local_skill_id"].(string)
	workspace, workspaceOK := msg.Payload["workspace_generation"].(string)
	bot, err := strconv.ParseInt(botText, 10, 64)
	if len(msg.Payload) != 3 || !botOK || !localOK || !workspaceOK || err != nil || bot <= 0 || strconv.FormatInt(bot, 10) != botText ||
		msg.TargetOwnerUserID != formatUID(client.uid) || msg.Result != nil || msg.Error != nil ||
		(msg.DeviceID != "" && msg.DeviceID != msg.TargetDeviceID) {
		fail("publication.invalid_request")
		return
	}
	r.actor, r.bot, r.device, r.local, r.workspace = client.uid, bot, msg.TargetDeviceID, local, workspace
	// Validate every caller-controlled selector BEFORE asynchronous authority IO.
	check := store.SkillHubPublicationEntryInput{ActorUID: r.actor, BotUID: bot, RequestID: r.requestID, Entrypoint: "workspace", Source: store.SkillHubPublicationSource{
		DeviceID: r.device, BodyID: "placeholder", InstallationID: "placeholder", RouteGeneration: 1, LocalSkillID: local, WorkspaceGeneration: workspace}}
	if check.Validate() != nil {
		fail("publication.invalid_request")
		return
	}
	r.expires = time.Now().Add(10 * time.Second)
	if msg.ExpiresAt > 0 && time.UnixMilli(msg.ExpiresAt).Before(r.expires) {
		r.expires = time.UnixMilli(msg.ExpiresAt)
	}
	if !time.Now().Before(r.expires) {
		fail("publication.invalid_request")
		return
	}
	select {
	case h.publicationAdmissions <- struct{}{}:
	default:
		fail("publication.busy")
		return
	}
	// Do not block the WebSocket loop (including unrelated model changes) on DB
	// work. The process-wide bound has no waiting queue; shutdown cancels DB IO.
	go func() {
		defer func() { <-h.publicationAdmissions }()
		ctx, cancel := context.WithDeadline(w.ctx, r.expires)
		defer cancel()
		h.admitPublicationWorkspace(ctx, r, entries, reader)
	}()
}

func (h *Hub) admitPublicationWorkspace(ctx context.Context, r publicationIntakeRequest, entries store.SkillHubPublicationEntryStore, reader store.SkillHubPublicationEntryRequestReader) {
	fail := func(code string) { h.publicationIntakeReply(r, nil, code) }
	source, route, role, code := h.publicationWorkspaceBinding(r.client, r.bot, r.device)
	if code != "" {
		fail(code)
		return
	}
	source.LocalSkillID, source.WorkspaceGeneration = r.local, r.workspace
	in := store.SkillHubPublicationEntryInput{RequestID: r.requestID, ActorUID: r.actor, BotUID: r.bot, Entrypoint: "workspace", Source: source}
	if in.Validate() != nil || !h.clientRoute(r.client).matches(r.route) {
		fail("publication.invalid_request")
		return
	}
	previous, err := reader.GetSkillHubPublicationEntryByRequest(ctx, r.actor, r.requestID)
	if err != nil {
		fail("publication.admission_uncertain")
		return
	}
	if previous != nil {
		// Reconnect is not a new intent. All immutable selectors must still match;
		// the original connection generation remains evidence, never renewed rights.
		expected := in
		expected.Source.RouteGeneration = previous.Input.Source.RouteGeneration
		if previous.Input != expected || previous.Input.Validate() != nil || source.RouteGeneration < expected.Source.RouteGeneration {
			fail("publication.request_conflict")
			return
		}
		in = expected
	}
	// No direct probe or thin-tool pending record is created in this path.
	// Recheck just before the transaction. The transaction itself locks/checks
	// current human/owner/body; background delivery rechecks the full runtime.
	current, currentRoute, currentRole, code := h.publicationWorkspaceBinding(r.client, r.bot, r.device)
	current.LocalSkillID, current.WorkspaceGeneration = r.local, r.workspace
	if code != "" || current != source || !currentRoute.matches(route) || currentRole != role || !h.clientRoute(r.client).matches(r.route) {
		fail("publication.runtime_changed")
		return
	}
	if ctx.Err() != nil {
		fail("publication.admission_uncertain")
		return
	}
	entry, replay, err := entries.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy())
	if err != nil {
		fail(publicationAdmissionErrorCode(err))
		return
	}
	if entry == nil || !skillHubPublicationEntryID.MatchString(entry.ID) || entry.Input != in || (entry.State != "waiting_runtime" && entry.State != "cancelled") {
		fail("publication.admission_uncertain")
		return
	}
	// Historical admission evidence belongs to the original active publisher.
	// Runtime/owner changes after commit cannot undo it. Never require the Bot
	// to stay connected just to acknowledge a committed, unverified intent.
	actor, err := h.db.GetUser(r.actor)
	if err != nil || actor == nil || actor.State != 0 || actor.AccountType != types.AccountHuman {
		fail("publication.admission_uncertain")
		return
	}
	h.publicationIntakeReply(r, map[string]any{"schema_version": 2, "entry_request_id": entry.ID, "state": entry.State, "replay": replay}, "")
}

func publicationAdmissionErrorCode(err error) string {
	switch {
	case errors.Is(err, store.ErrSkillHubPublicationDenied):
		return "publication.owner_required"
	case errors.Is(err, store.ErrSkillHubPublicationConflict):
		return "publication.request_conflict"
	case errors.Is(err, store.ErrSkillHubPublicationInvalid):
		return "publication.invalid_request"
	case errors.Is(err, store.ErrSkillHubPublicationCapacity):
		return "publication.capacity"
	case errors.Is(err, store.ErrSkillHubPublicationRateLimit):
		return "publication.rate_limited"
	default:
		return "publication.admission_uncertain"
	}
}
