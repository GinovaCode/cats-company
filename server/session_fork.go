package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (h *ConversationHandler) HandleFork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	db, ok := h.db.(store.SessionForkStore)
	if !ok {
		writeJSON(w, 503, map[string]string{"error": "session_fork_unavailable"})
		return
	}
	uid := UIDFromContext(r.Context())
	user, err := h.db.GetUser(uid)
	if err != nil || user == nil || user.AccountType != types.AccountHuman {
		writeJSON(w, 403, map[string]string{"error": "session_fork_requires_human"})
		return
	}
	if r.Method == http.MethodGet {
		request, topic := r.URL.Query().Get("request_id"), r.URL.Query().Get("topic_id")
		if (request == "") == (topic == "") {
			writeJSON(w, 400, map[string]string{"error": "provide_request_id_or_topic_id"})
			return
		}
		result, err := db.GetSessionFork(r.Context(), uid, request, topic)
		writeSessionFork(w, result, err)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	var req types.SessionForkRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	req.RequestID, req.SourceTopicID, req.Name = strings.TrimSpace(req.RequestID), strings.TrimSpace(req.SourceTopicID), strings.TrimSpace(req.Name)
	if req.RequestID == "" || len(req.RequestID) > 128 || containsControlCharacter(req.RequestID) || req.SourceTopicID == "" || len(req.SourceTopicID) > 64 || utf8.RuneCountInString(req.Name) > 80 {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	// Lookup precedes online/source checks: a lost successful response stays successful.
	existing, err := db.GetSessionFork(r.Context(), uid, req.RequestID, "")
	if err == nil {
		if existing.SourceTopicID != req.SourceTopicID || existing.Name != req.Name {
			writeSessionFork(w, nil, store.ErrSessionForkConflict)
			return
		}
		if existing.State == "pending" {
			h.dispatchSessionFork(existing)
		}
		writeSessionFork(w, existing, nil)
		return
	}
	if !errors.Is(err, store.ErrSessionForkNotFound) {
		writeSessionFork(w, nil, err)
		return
	}
	agent, err := h.forkSourceAgent(uid, req.SourceTopicID)
	if err != nil {
		writeSessionFork(w, nil, err)
		return
	}
	if h.hub == nil || !h.hub.BotBodyStatus(agent).Active {
		writeJSON(w, 409, map[string]string{"error": "session_fork_agent_offline"})
		return
	}
	if statuses, ok := h.db.(store.ConversationTaskStatusStore); ok {
		values, err := statuses.GetConversationTaskStatuses([]string{req.SourceTopicID})
		if err != nil {
			writeSessionFork(w, nil, err)
			return
		}
		// A stale cloud status may outlive a completed local turn. Let the
		// runtime's locked idle/interruption checks decide whether it can fork.
		if s := values[req.SourceTopicID]; s != nil && (s.State == "running" || s.State == "waiting") {
			writeJSON(w, 409, map[string]string{"error": "session_fork_source_busy"})
			return
		}
	}
	req.ActorUserID, req.AgentUID, req.BodyID = uid, agent, h.hub.BotBodyStatus(agent).BodyID
	result, err := db.CreateSessionFork(r.Context(), req)
	if err == nil && result.State == "pending" {
		h.dispatchSessionFork(result)
	}
	writeSessionFork(w, result, err)
}

func (h *ConversationHandler) forkSourceAgent(uid int64, topic string) (int64, error) {
	var agent int64
	if strings.HasPrefix(topic, "p2p_") {
		agent = extractPeerUID(topic, uid)
		if agent <= 0 || topic != p2pTopicID(uid, agent) || agent == uid {
			return 0, store.ErrSessionForkForbidden
		}
	} else if isGroupTopic(topic) {
		group, err := h.db.GetGroup(extractGroupID(topic))
		if err != nil || group == nil || topic != "grp_"+formatInt64(group.ID) || group.OwnerID != uid || group.Kind != types.GroupKindAgentTask || group.MemberCount != 2 || len(group.AgentIDs) != 1 {
			return 0, store.ErrSessionForkForbidden
		}
		member, err := h.db.IsGroupMember(group.ID, uid)
		if err != nil || !member {
			return 0, store.ErrSessionForkForbidden
		}
		agent = group.AgentIDs[0]
	} else {
		return 0, store.ErrSessionForkForbidden
	}
	owner, err := h.db.GetBotOwner(agent)
	if err != nil || owner != uid {
		return 0, store.ErrSessionForkForbidden
	}
	return agent, nil
}

func (h *ConversationHandler) dispatchSessionFork(f *types.SessionFork) {
	if h.hub == nil || h.hub.bodyLeases == nil {
		return
	}
	status := h.hub.BotBodyStatus(f.AgentUID)
	if !status.Active || status.BodyID != f.BodyID {
		return
	}
	for _, client := range h.hub.getClients(f.AgentUID) {
		if client.accountType == types.AccountBot && client.deviceConnector == nil && client.bodyID == f.BodyID && h.hub.bodyLeases.isCurrent(client.uid, client.bodyID, client.connectionID) {
			h.hub.SendToClient(client, &ServerMessage{SessionFork: f.Control()})
			return
		}
	}
}

// HandleBotFork is API-key-only. A 404 means an ordinary non-fork target;
// all other failures must fail closed in missing-local-session recovery.
func (h *ConversationHandler) HandleBotFork(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	db, ok := h.db.(store.SessionForkStore)
	if !ok {
		writeJSON(w, 503, map[string]string{"error": "session_fork_unavailable"})
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	topic := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	if topic == "" {
		writeJSON(w, 400, map[string]string{"error": "topic_id_required"})
		return
	}
	result, err := db.GetBotSessionFork(r.Context(), UIDFromContext(r.Context()), topic)
	writeSessionFork(w, result, err)
}

func (h *ConversationHandler) HandleForkComplete(w http.ResponseWriter, r *http.Request) {
	db, ok := h.db.(store.SessionForkStore)
	if !ok {
		writeJSON(w, 503, map[string]string{"error": "session_fork_unavailable"})
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		writeJSON(w, 405, map[string]string{"error": "method_not_allowed"})
		return
	}
	var req types.SessionForkCompletion
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil || req.RequestID == "" || len(req.RequestID) > 128 || req.TargetTopicID == "" || (req.State != "ready" && req.State != "failed") || len(req.Error) > 2000 {
		writeJSON(w, 400, map[string]string{"error": "invalid_request"})
		return
	}
	req.BodyID = strings.TrimSpace(r.Header.Get(botBodyIDHeader))
	result, err := db.CompleteSessionFork(r.Context(), UIDFromContext(r.Context()), req)
	writeSessionFork(w, result, err)
}

func writeSessionFork(w http.ResponseWriter, f *types.SessionFork, err error) {
	if err == nil {
		writeJSON(w, 200, f)
		return
	}
	code, text := 500, "session_fork_unavailable"
	switch {
	case errors.Is(err, store.ErrSessionForkNotFound):
		code, text = 404, err.Error()
	case errors.Is(err, store.ErrSessionForkForbidden):
		code, text = 403, err.Error()
	case errors.Is(err, store.ErrSessionForkConflict), errors.Is(err, store.ErrSessionForkSource):
		code, text = 409, err.Error()
	}
	writeJSON(w, code, map[string]string{"error": text})
}

func sessionForkPublishError(db store.Store, topic string) (int, string) {
	forks, ok := db.(store.SessionForkStore)
	if !ok {
		return 0, ""
	}
	writable, err := forks.SessionForkWritable(context.Background(), topic)
	if err != nil {
		return 503, "session_fork_state_unavailable"
	}
	if !writable {
		return 409, "session_fork_not_ready"
	}
	return 0, ""
}
