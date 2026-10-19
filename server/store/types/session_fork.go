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
