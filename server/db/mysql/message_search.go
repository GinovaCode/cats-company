package mysql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Leave time for MySQL's timeout error to reach the handler's eight-second budget.
const mysqlMessageSearchMaxExecutionTime = 7500 * time.Millisecond
const mysqlMessageSearchExecutionHint = "MAX_EXECUTION_TIME(7500)"

// UNION ALL computes permissions once. The lateral lookup keeps only each
// topic's newest pageSize+offset hits before globally merging the same window.
// Sender and content filters must run before the per-topic limit. CASE keeps
// full block-schema validation off the path for messages with no visible match.
// Keep validation in WHERE and use indexed sender lookups: a projected subquery
// or hash join would materialize full histories before the ordered limit. The
// constant topic_id also appears in the inner ORDER BY so MySQL can reverse-scan
// the existing (topic_id, created_at) index with its implicit primary-key suffix.
const mysqlMessageSearchQuery = `
WITH search_params AS (
  SELECT ? AS search_type, LOWER(?) AS needle, ? AS viewer_uid
),
readable_topics AS (
  SELECT t.id, COALESCE(g.name, t.name, '') AS topic_name
  FROM search_params search
  JOIN users viewer ON viewer.id = search.viewer_uid
  JOIN group_members gm ON gm.user_id = viewer.id
  JOIN ` + "`groups`" + ` g ON g.id = gm.group_id
  JOIN topics t ON t.type = 'group' AND t.id = CONCAT('grp_', gm.group_id)
  WHERE gm.user_id IS NOT NULL
    AND (viewer.account_type <> 'human' OR COALESCE(g.group_kind, 'standard') <> 'channel_managed')
  UNION ALL
  SELECT t.id, COALESCE(NULLIF(ct.title, ''), NULLIF(peer.display_name, ''), peer.username, t.name, '')
  FROM search_params search
  JOIN users viewer ON viewer.id = search.viewer_uid
  JOIN users peer ON peer.id <> viewer.id
  JOIN topics t ON t.type = 'p2p'
    AND t.id = CONCAT('p2p_', LEAST(viewer.id, peer.id), '_', GREATEST(viewer.id, peer.id))
  LEFT JOIN bot_config peer_bot ON peer_bot.user_id = peer.id
  LEFT JOIN conversation_titles ct ON ct.user_id = viewer.id AND ct.topic_id = t.id
  WHERE peer.id IS NOT NULL AND (
    viewer.account_type = 'bot' OR peer.account_type <> 'bot' OR peer_bot.owner_id = viewer.id OR EXISTS (
      SELECT 1 FROM friends f
      WHERE f.from_user_id = viewer.id AND f.to_user_id = peer.id AND f.status = 'accepted'
    )
  )
)
SELECT /*+ ` + mysqlMessageSearchExecutionHint + ` */ m.id, m.topic_id, readable.topic_name,
       m.from_uid, m.sender_name, m.content, m.msg_type, m.created_at, m.content_blocks
FROM readable_topics readable
CROSS JOIN search_params search
JOIN LATERAL (
SELECT /*+ NO_BNL(sender) */ m.id, m.topic_id,
       m.from_uid, COALESCE(NULLIF(sender.display_name, ''), sender.username, '') AS sender_name,
       m.content, m.msg_type, m.created_at, m.content_blocks
FROM messages m
STRAIGHT_JOIN users sender ON sender.id = m.from_uid
WHERE m.topic_id = readable.id AND sender.account_type IN ('human', 'bot')
AND CASE WHEN (
  (search.search_type <> 'artifact' AND m.msg_type = 'text'
    AND (
      EXISTS (
        SELECT 1 FROM JSON_TABLE(
          COALESCE(m.content_blocks, JSON_ARRAY()),
          '$[*]' COLUMNS (
            block_type LONGTEXT PATH '$.type',
            block_text LONGTEXT PATH '$.text',
            block_content LONGTEXT PATH '$.content'
          )
        ) AS block
        WHERE BINARY block.block_type IN ('text', 'assistant_text')
          AND LOCATE(search.needle, LOWER(COALESCE(
            CASE WHEN OCTET_LENGTH(block.block_text) > 0 THEN block.block_text ELSE block.block_content END, ''
          ))) > 0
      )
      OR (
        LOCATE(search.needle, LOWER(m.content)) > 0
        AND NOT EXISTS (
          SELECT 1 FROM JSON_TABLE(
            COALESCE(m.content_blocks, JSON_ARRAY()),
            '$[*]' COLUMNS (
              block_type LONGTEXT PATH '$.type',
              block_text LONGTEXT PATH '$.text',
              block_content LONGTEXT PATH '$.content'
            )
          ) AS block
          WHERE BINARY block.block_type IN ('thinking', 'tool_use', 'tool_result', 'runtime_plan', 'debug')
            OR (BINARY block.block_type IN ('text', 'assistant_text')
              AND OCTET_LENGTH(COALESCE(
                CASE WHEN OCTET_LENGTH(block.block_text) > 0 THEN block.block_text ELSE block.block_content END, ''
              )) > 0)
        )
      )
    ))
  OR
  (search.search_type <> 'message' AND (
    (m.content_blocks IS NOT NULL AND JSON_TYPE(m.content_blocks) = 'ARRAY'
      AND EXISTS (
      SELECT 1
      FROM JSON_TABLE(
        COALESCE(m.content_blocks, JSON_ARRAY()),
        '$[*]' COLUMNS (
          block_type VARCHAR(32) PATH '$.type',
          name VARCHAR(2048) PATH '$.name',
          payload_name VARCHAR(2048) PATH '$.payload.name',
          payload_file_name VARCHAR(2048) PATH '$.payload.file_name',
          payload_filename VARCHAR(2048) PATH '$.payload.filename',
          payload_title VARCHAR(2048) PATH '$.payload.title'
        )
      ) AS artifact
      WHERE artifact.block_type IN ('file', 'image', 'audio', 'video')
        AND (
          LOCATE(search.needle, LOWER(COALESCE(artifact.name, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_name, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_file_name, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_filename, ''))) > 0
          OR LOCATE(search.needle, LOWER(COALESCE(artifact.payload_title, ''))) > 0
        )
    ))
    OR (m.msg_type = 'file' AND EXISTS (
      SELECT 1
      FROM LATERAL (
        SELECT CASE
          WHEN NOT JSON_VALID(m.content) THEN JSON_OBJECT()
          WHEN JSON_TYPE(m.content) = 'STRING' AND JSON_VALID(JSON_UNQUOTE(m.content))
            THEN JSON_UNQUOTE(m.content)
          ELSE m.content
        END AS content
      ) AS legacy_content
      JOIN LATERAL (
        SELECT CASE
          WHEN JSON_TYPE(JSON_EXTRACT(legacy_content.content, '$.payload')) = 'OBJECT'
            THEN JSON_EXTRACT(legacy_content.content, '$.payload')
          ELSE legacy_content.content
        END AS fields
      ) AS legacy ON TRUE
      JOIN JSON_TABLE(
        JSON_EXTRACT(legacy.fields, '$'),
        '$' COLUMNS (
          name VARCHAR(2048) PATH '$.name',
          file_name VARCHAR(2048) PATH '$.file_name',
          filename VARCHAR(2048) PATH '$.filename',
          title VARCHAR(2048) PATH '$.title'
        )
      ) AS legacy_file ON TRUE
      WHERE (JSON_TYPE(JSON_EXTRACT(legacy.fields, '$.name')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.name, ''))) > 0)
        OR (JSON_TYPE(JSON_EXTRACT(legacy.fields, '$.file_name')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.file_name, ''))) > 0)
        OR (JSON_TYPE(JSON_EXTRACT(legacy.fields, '$.filename')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.filename, ''))) > 0)
        OR (JSON_TYPE(JSON_EXTRACT(legacy.fields, '$.title')) = 'STRING'
          AND LOCATE(search.needle, LOWER(COALESCE(legacy_file.title, ''))) > 0)
    ))
  ))
) THEN CASE
  WHEN m.content_blocks IS NULL THEN TRUE
  ELSE JSON_SCHEMA_VALID(
    '{"type":["array","null"],"items":{"type":["object","null"],"properties":{"type":{"type":["string","null"]},"text":{"type":["string","null"]},"thinking":{"type":["string","null"]},"payload":{"type":["object","null"],"properties":{"name":{"type":["string","null"]},"file_name":{"type":["string","null"]},"filename":{"type":["string","null"]},"title":{"type":["string","null"]}}},"id":{"type":["string","null"]},"name":{"type":["string","null"]},"input":{"type":["object","null"]},"tool_use_id":{"type":["string","null"]},"content":{"type":["string","null"]},"is_error":{"type":["boolean","null"]}}}}',
    m.content_blocks
  )
  AND NOT EXISTS (
    SELECT 1
    FROM JSON_TABLE(
      m.content_blocks,
      '$[*]' COLUMNS (block_index FOR ORDINALITY)
    ) AS typed_block
    JOIN JSON_TABLE(
      JSON_KEYS(JSON_EXTRACT(
        m.content_blocks,
        CONCAT('$[', typed_block.block_index - 1, ']')
      )),
      '$[*]' COLUMNS (block_key VARCHAR(64) PATH '$')
    ) AS typed_key
    WHERE BINARY LOWER(typed_key.block_key) IN (
      'type', 'text', 'thinking', 'payload', 'id',
      'name', 'input', 'tool_use_id', 'content', 'is_error'
    )
    AND BINARY typed_key.block_key NOT IN (
      'type', 'text', 'thinking', 'payload', 'id',
      'name', 'input', 'tool_use_id', 'content', 'is_error'
    )
  )
END ELSE FALSE END
ORDER BY m.topic_id DESC, m.created_at DESC, m.id DESC
LIMIT ?
) m ON TRUE
ORDER BY m.created_at DESC, m.id DESC
LIMIT ? OFFSET ?`

// mysqlMessageSearchQueryForContext also bounds execution inside MySQL. The
// driver closes its connection on context cancellation, which does not stop an
// executing statement on the server. Recompute the remaining budget per page.
func mysqlMessageSearchQueryForContext(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	budget := mysqlMessageSearchMaxExecutionTime
	if deadline, ok := ctx.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < budget {
			budget = remaining
		}
	}
	// MySQL interprets zero as unlimited. Round a sub-millisecond budget up to
	// one rather than accidentally disabling the server-side limit.
	milliseconds := max(int64(1), budget.Milliseconds())
	hint := fmt.Sprintf("MAX_EXECUTION_TIME(%d)", milliseconds)
	return strings.Replace(mysqlMessageSearchQuery, mysqlMessageSearchExecutionHint, hint, 1), nil
}

func mysqlMessageSearchError(err error) error {
	var mysqlErr *mysqldriver.MySQLError
	if errors.As(err, &mysqlErr) && mysqlErr.Number == 3024 {
		return fmt.Errorf("message search execution limit: %w: %w", context.DeadlineExceeded, err)
	}
	return err
}

// SearchMessages searches only topics readable by viewerUID. Access filtering is atomic with selection.
func (a *Adapter) SearchMessages(ctx context.Context, viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	return store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
		statement, err := mysqlMessageSearchQueryForContext(ctx)
		if err != nil {
			return nil, 0, err
		}
		rows, err := a.db.QueryContext(ctx, statement, searchType, query, viewerUID, pageSize+offset, pageSize, offset)
		if err != nil {
			return nil, 0, fmt.Errorf("search messages: %w", mysqlMessageSearchError(err))
		}
		page, scanned, scanErr := scanMySQLMessageSearch(rows, query, searchType, remaining)
		closeErr := rows.Close()
		if scanErr != nil {
			return nil, scanned, mysqlMessageSearchError(scanErr)
		}
		if closeErr != nil {
			return nil, scanned, fmt.Errorf("close message search rows: %w", mysqlMessageSearchError(closeErr))
		}
		return page, scanned, nil
	})
}

func scanMySQLMessageSearch(rows *sql.Rows, query, searchType string, limit int) ([]*store.MessageSearchResult, int, error) {
	results := make([]*store.MessageSearchResult, 0, limit)
	scanned := 0
	for rows.Next() {
		scanned++
		var (
			result     store.MessageSearchResult
			msgType    string
			blocksJSON []byte
		)
		if err := rows.Scan(&result.MessageID, &result.TopicID, &result.TopicName, &result.FromUID,
			&result.SenderName, &result.Content, &msgType, &result.CreatedAt, &blocksJSON); err != nil {
			return nil, scanned, fmt.Errorf("scan message search result: %w", err)
		}
		match, ok := store.MatchMessageSearchCandidate(store.MessageSearchCandidate{
			Result:        result,
			MessageType:   msgType,
			ContentBlocks: blocksJSON,
		}, query, searchType)
		if !ok {
			continue
		}
		results = append(results, match)
		if len(results) == limit {
			break
		}
	}
	return results, scanned, rows.Err()
}

// GetMessagesAround returns a bounded chronological window around a message in one topic.
func (a *Adapter) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	beforeLimit := (limit + 1) / 2
	afterLimit := limit - beforeLimit
	rows, err := a.db.Query(`
SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to FROM (
  SELECT * FROM (
    SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to
    FROM messages WHERE topic_id = ? AND id <= ? ORDER BY id DESC LIMIT ?
  ) before_messages
  UNION ALL
  SELECT * FROM (
    SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to
    FROM messages WHERE topic_id = ? AND id > ? ORDER BY id ASC LIMIT ?
  ) after_messages
) around_messages ORDER BY id ASC`, topicID, messageID, beforeLimit, topicID, messageID, afterLimit)
	if err != nil {
		return nil, fmt.Errorf("get messages around: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan message around")
}
