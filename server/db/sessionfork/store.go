// Package sessionfork implements the shared fork transaction for both SQL stores.
package sessionfork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type SQLStore struct {
	DB       *sql.DB
	Postgres bool
}

func (s SQLStore) query(q string) string {
	if !s.Postgres {
		return strings.ReplaceAll(q, `"groups"`, "`groups`")
	}
	n := 0
	var b strings.Builder
	for _, r := range q {
		if r == '?' {
			n++
			fmt.Fprintf(&b, "$%d", n)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

const forkColumns = `actor_user_id, request_id, source_topic_id, target_topic_id, agent_uid, body_id, request_name, source_revision, target_revision, source_last_seq, message_id_map, state, error, created_at`

func scanFork(row interface{ Scan(...interface{}) error }) (*types.SessionFork, error) {
	f := &types.SessionFork{}
	var mapping []byte
	err := row.Scan(&f.ActorUserID, &f.RequestID, &f.SourceTopicID, &f.TargetTopicID, &f.AgentUID, &f.BodyID, &f.Name, &f.SourceRevision, &f.TargetRevision, &f.SourceLastSeq, &mapping, &f.State, &f.Error, &f.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSessionForkNotFound
	}
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(mapping, &f.MessageIDMap); err != nil {
		return nil, err
	}
	return f, nil
}

func (s SQLStore) GetSessionFork(ctx context.Context, uid int64, request, topic string) (*types.SessionFork, error) {
	column, value := "request_id", request
	if request == "" {
		column, value = "target_topic_id", topic
	}
	return s.get(ctx, "actor_user_id = ? AND "+column+" = ?", uid, value)
}
func (s SQLStore) GetBotSessionFork(ctx context.Context, uid int64, topic string) (*types.SessionFork, error) {
	return s.get(ctx, "agent_uid = ? AND target_topic_id = ?", uid, topic)
}
func (s SQLStore) get(ctx context.Context, where string, args ...interface{}) (*types.SessionFork, error) {
	f, err := scanFork(s.DB.QueryRowContext(ctx, s.query("SELECT "+forkColumns+" FROM session_forks WHERE "+where), args...))
	return f, err
}

func (s SQLStore) SessionForkWritable(ctx context.Context, topic string) (bool, error) {
	var state string
	err := s.DB.QueryRowContext(ctx, s.query("SELECT fork_state FROM topics WHERE id = ?"), topic).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return state != "pending" && state != "failed", err
}

func (s SQLStore) CreateSessionFork(ctx context.Context, req types.SessionForkRequest) (*types.SessionFork, error) {
	if req.ActorUserID <= 0 || req.AgentUID <= 0 || req.RequestID == "" || req.BodyID == "" {
		return nil, store.ErrSessionForkForbidden
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Serialize user-scoped request IDs before allocating a group or copying rows.
	var actorType string
	err = tx.QueryRowContext(ctx, s.query("SELECT account_type FROM users WHERE id = ? AND state = 0 FOR UPDATE"), req.ActorUserID).Scan(&actorType)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) || actorType != "human" {
		return nil, store.ErrSessionForkForbidden
	}
	existing, err := scanFork(tx.QueryRowContext(ctx, s.query("SELECT "+forkColumns+" FROM session_forks WHERE actor_user_id = ? AND request_id = ? FOR UPDATE"), req.ActorUserID, req.RequestID))
	if err == nil {
		if existing.SourceTopicID != req.SourceTopicID || existing.Name != req.Name {
			return nil, store.ErrSessionForkConflict
		}
		return existing, tx.Commit()
	}
	if !errors.Is(err, store.ErrSessionForkNotFound) {
		return nil, err
	}
	var revision int64
	var state string
	err = tx.QueryRowContext(ctx, s.query("SELECT history_revision, fork_state FROM topics WHERE id = ? FOR UPDATE"), req.SourceTopicID).Scan(&revision, &state)
	if errors.Is(err, sql.ErrNoRows) || state == "pending" || state == "failed" {
		return nil, store.ErrSessionForkSource
	}
	if err != nil {
		return nil, err
	}
	if err = s.validateSource(ctx, tx, req.ActorUserID, req.AgentUID, req.SourceTopicID, req.BodyID); err != nil {
		return nil, err
	}
	messages, err := s.readMessages(ctx, tx, req.SourceTopicID)
	if err != nil {
		return nil, err
	}
	for _, m := range messages {
		if m.from != req.ActorUserID && m.from != req.AgentUID {
			return nil, store.ErrSessionForkForbidden
		}
	}
	title := req.Name
	if title == "" {
		title = "\u4f1a\u8bdd\u526f\u672c"
	}
	groupID, err := s.insertID(ctx, tx, `INSERT INTO "groups" (name, owner_id, group_kind) VALUES (?, ?, 'agent_task')`, title, req.ActorUserID)
	if err != nil {
		return nil, err
	}
	target := "grp_" + strconv.FormatInt(groupID, 10)
	_, err = tx.ExecContext(ctx, s.query("INSERT INTO group_members (group_id,user_id,role) VALUES (?,?,'owner'),(?,?,'member')"), groupID, req.ActorUserID, groupID, req.AgentUID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, s.query("INSERT INTO topics (id,type,name,owner_id) VALUES (?,'group',?,?)"), target, title, req.ActorUserID)
	if err != nil {
		return nil, err
	}
	f := &types.SessionFork{ActorUserID: req.ActorUserID, AgentUID: req.AgentUID, RequestID: req.RequestID, SourceTopicID: req.SourceTopicID, TargetTopicID: target, Name: req.Name, BodyID: req.BodyID, SourceRevision: revision, State: "pending", CreatedAt: time.Now().UTC(), MessageIDMap: map[string]int64{}}
	// Copy raw stored payloads, never run the message-ingestion/fanout service.
	// client_msg_id belongs to ingress deduplication and must not be inherited.
	for _, m := range messages {
		id, err := s.insertID(ctx, tx, "INSERT INTO messages (topic_id,from_uid,content,msg_type,content_blocks,mode,role,metadata,created_at) VALUES (?,?,?,?,?,?,?,?,?)", target, m.from, m.content, m.msgType, nullable(m.blocks), nullable(m.mode), nullable(m.role), nullable(m.metadata), m.created)
		if err != nil {
			return nil, err
		}
		f.MessageIDMap[strconv.FormatInt(m.id, 10)] = id
		f.SourceLastSeq = m.id
	}
	// Second pass handles forward replies as well as ordinary backward replies.
	for _, m := range messages {
		var reply interface{}
		if m.reply.Valid && m.reply.Int64 != 0 {
			mapped, ok := f.MessageIDMap[strconv.FormatInt(m.reply.Int64, 10)]
			if !ok {
				return nil, store.ErrSessionForkSource
			}
			reply = mapped
		}
		metadata, err := RemapMetadata(m.metadata, req.SourceTopicID, target, f.MessageIDMap)
		if err != nil {
			return nil, err
		}
		_, err = tx.ExecContext(ctx, s.query("UPDATE messages SET reply_to = ?, metadata = ? WHERE id = ?"), reply, nullable(metadata), f.MessageIDMap[strconv.FormatInt(m.id, 10)])
		if err != nil {
			return nil, err
		}
	}
	mapping, err := json.Marshal(f.MessageIDMap)
	if err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(ctx, s.query("SELECT history_revision FROM topics WHERE id = ?"), target).Scan(&f.TargetRevision); err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, s.query("INSERT INTO session_forks ("+forkColumns+") VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)"), f.ActorUserID, f.RequestID, f.SourceTopicID, f.TargetTopicID, f.AgentUID, f.BodyID, f.Name, f.SourceRevision, f.TargetRevision, f.SourceLastSeq, string(mapping), f.State, "", f.CreatedAt)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, s.query("UPDATE topics SET fork_state = 'pending' WHERE id = ?"), target)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return f, nil
}

func (s SQLStore) validateSource(ctx context.Context, tx *sql.Tx, actor, agent int64, topic, body string) error {
	var owner sql.NullInt64
	var bound sql.NullString
	var account string
	var userState int
	err := tx.QueryRowContext(ctx, s.query("SELECT b.owner_id,b.body_id,u.account_type,u.state FROM bot_config b JOIN users u ON u.id=b.user_id WHERE b.user_id = ?"), agent).Scan(&owner, &bound, &account, &userState)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrSessionForkForbidden
	}
	if err != nil {
		return err
	}
	if account != "bot" || userState != 0 || bound.String != body {
		return store.ErrSessionForkForbidden
	}
	if !owner.Valid || owner.Int64 != actor {
		// Recheck the existing friendship inside both creation and completion
		// transactions. No new sharing grant is introduced by copying a session.
		var friends int
		err = tx.QueryRowContext(ctx, s.query("SELECT COUNT(*) FROM friends WHERE from_user_id = ? AND to_user_id = ? AND status = 'accepted'"), actor, agent).Scan(&friends)
		if err != nil {
			return err
		}
		if friends == 0 {
			return store.ErrSessionForkForbidden
		}
	}
	if strings.HasPrefix(topic, "p2p_") {
		lo, hi := actor, agent
		if lo > hi {
			lo, hi = hi, lo
		}
		if topic != fmt.Sprintf("p2p_%d_%d", lo, hi) || actor == agent {
			return store.ErrSessionForkForbidden
		}
		return nil
	}
	gid, err := strconv.ParseInt(strings.TrimPrefix(topic, "grp_"), 10, 64)
	if err != nil || topic != fmt.Sprintf("grp_%d", gid) {
		return store.ErrSessionForkForbidden
	}
	rows, err := tx.QueryContext(ctx, s.query("SELECT user_id FROM group_members WHERE group_id = ? ORDER BY user_id"), gid)
	if err != nil {
		return err
	}
	defer rows.Close()
	count := 0
	actorFound, agentFound := false, false
	for rows.Next() {
		var uid int64
		if err = rows.Scan(&uid); err != nil {
			return err
		}
		count++
		actorFound = actorFound || uid == actor
		agentFound = agentFound || uid == agent
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if count != 2 || !actorFound || !agentFound {
		return store.ErrSessionForkForbidden
	}
	return nil
}

func (s SQLStore) CompleteSessionFork(ctx context.Context, agent int64, req types.SessionForkCompletion) (*types.SessionFork, error) {
	if req.State != "ready" && req.State != "failed" {
		return nil, store.ErrSessionForkConflict
	}
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelSerializable})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	f, err := scanFork(tx.QueryRowContext(ctx, s.query("SELECT "+forkColumns+" FROM session_forks WHERE agent_uid = ? AND target_topic_id = ? AND request_id = ? FOR UPDATE"), agent, req.TargetTopicID, req.RequestID))
	if err != nil {
		return nil, err
	}
	// Acknowledgement retries must never revalidate a terminal snapshot against
	// a source that is allowed to have advanced since the successful commit.
	if f.State != "pending" {
		return f, tx.Commit()
	}
	if req.BodyID != f.BodyID {
		return nil, store.ErrSessionForkForbidden
	}
	var revision int64
	var sourceState string
	err = tx.QueryRowContext(ctx, s.query("SELECT history_revision,fork_state FROM topics WHERE id = ? FOR UPDATE"), f.SourceTopicID).Scan(&revision, &sourceState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	failure := ""
	if errors.Is(err, sql.ErrNoRows) || revision != f.SourceRevision || sourceState == "pending" || sourceState == "failed" {
		failure = "source_changed_retry_needed"
	}
	if failure == "" {
		if err = s.validateSource(ctx, tx, f.ActorUserID, f.AgentUID, f.SourceTopicID, f.BodyID); err != nil {
			if !errors.Is(err, store.ErrSessionForkForbidden) {
				return nil, err
			}
			failure = "source_or_bot_binding_changed"
		}
	}
	var targetState string
	var targetRevision int64
	err = tx.QueryRowContext(ctx, s.query("SELECT history_revision,fork_state FROM topics WHERE id = ? FOR UPDATE"), f.TargetTopicID).Scan(&targetRevision, &targetState)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if errors.Is(err, sql.ErrNoRows) || targetState != "pending" || targetRevision != f.TargetRevision {
		failure = "target_changed_retry_needed"
	}
	if failure == "" {
		if err = s.validateSource(ctx, tx, f.ActorUserID, f.AgentUID, f.TargetTopicID, f.BodyID); err != nil {
			if !errors.Is(err, store.ErrSessionForkForbidden) {
				return nil, err
			}
			failure = "target_changed_retry_needed"
		}
	}
	f.State, f.Error = req.State, req.Error
	if failure != "" {
		f.State, f.Error = "failed", failure
	}
	if f.State == "ready" {
		f.Error = ""
	} else if f.Error == "" {
		f.Error = "local_clone_failed"
	}
	_, err = tx.ExecContext(ctx, s.query("UPDATE session_forks SET state = ?, error = ? WHERE actor_user_id = ? AND request_id = ?"), f.State, f.Error, f.ActorUserID, f.RequestID)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, s.query("UPDATE topics SET fork_state = ? WHERE id = ?"), f.State, f.TargetTopicID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return f, nil
}

type message struct {
	id, from                     int64
	content, msgType             string
	blocks, mode, role, metadata sql.NullString
	reply                        sql.NullInt64
	created                      time.Time
}

func (s SQLStore) readMessages(ctx context.Context, tx *sql.Tx, topic string) ([]message, error) {
	rows, err := tx.QueryContext(ctx, s.query("SELECT id,from_uid,content,msg_type,content_blocks,mode,role,metadata,reply_to,created_at FROM messages WHERE topic_id = ? ORDER BY id"), topic)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var messages []message
	for rows.Next() {
		var m message
		if err = rows.Scan(&m.id, &m.from, &m.content, &m.msgType, &m.blocks, &m.mode, &m.role, &m.metadata, &m.reply, &m.created); err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}
func nullable(s sql.NullString) interface{} {
	if s.Valid {
		return s.String
	}
	return nil
}
func (s SQLStore) insertID(ctx context.Context, tx *sql.Tx, q string, args ...interface{}) (int64, error) {
	if s.Postgres {
		var id int64
		err := tx.QueryRowContext(ctx, s.query(q)+" RETURNING id", args...).Scan(&id)
		return id, err
	}
	result, err := tx.ExecContext(ctx, s.query(q), args...)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}
