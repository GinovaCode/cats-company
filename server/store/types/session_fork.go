package types

import "time"

type SessionForkRequest struct {
	RequestID     string `json:"request_id"`
	SourceTopicID string `json:"source_topic_id"`
	Name          string `json:"name,omitempty"`
	ActorUserID   int64  `json:"-"`
	AgentUID      int64  `json:"-"`
	BodyID        string `json:"-"`
}

// SessionFork is a durable operation, not a chat message or an execution request.
type SessionFork struct {
	RequestID      string           `json:"request_id"`
	SourceTopicID  string           `json:"source_topic_id"`
	TargetTopicID  string           `json:"target_topic_id"`
	State          string           `json:"state"`
	Error          string           `json:"error,omitempty"`
	ActorUserID    int64            `json:"-"`
	AgentUID       int64            `json:"-"`
	Name           string           `json:"-"`
	BodyID         string           `json:"-"`
	SourceRevision int64            `json:"-"`
	TargetRevision int64            `json:"-"`
	SourceLastSeq  int64            `json:"-"`
	MessageIDMap   map[string]int64 `json:"-"`
	CreatedAt      time.Time        `json:"-"`
}

// Terminal failure reasons the server records on its own behalf, as opposed to
// reasons reported by the runtime. Clients map these to actionable guidance
// instead of showing the raw code.
const (
	// SessionForkErrorAckTimeout marks a copy whose runtime never acknowledged
	// it within the acknowledgement window: typically a device that is offline,
	// or a runtime built before session forks existed. Such a runtime ignores
	// the control frame entirely, so without this bound the copy stays pending
	// forever and the UI waits on it indefinitely.
	SessionForkErrorAckTimeout = "session_fork_ack_timeout"
	// SessionForkErrorCancelled marks a copy the user abandoned while it was
	// still pending. A copy that already reached a terminal state is never
	// rewritten, so this can only ever replace a pending state.
	SessionForkErrorCancelled = "session_fork_cancelled"
)

type SessionForkCompletion struct {
	BodyID        string `json:"-"`
	RequestID     string `json:"request_id"`
	TargetTopicID string `json:"target_topic_id"`
	State         string `json:"state"`
	Error         string `json:"error,omitempty"`
}

type SessionForkControl struct {
	RequestID     string           `json:"request_id"`
	SourceTopicID string           `json:"source_topic_id"`
	TargetTopicID string           `json:"target_topic_id"`
	ActorUserID   int64            `json:"actor_user_id"`
	AgentUID      int64            `json:"agent_uid"`
	SourceLastSeq int64            `json:"source_last_seq"`
	MessageIDMap  map[string]int64 `json:"message_id_map"`
}

func (f *SessionFork) Control() *SessionForkControl {
	return &SessionForkControl{f.RequestID, f.SourceTopicID, f.TargetTopicID, f.ActorUserID, f.AgentUID, f.SourceLastSeq, f.MessageIDMap}
}
