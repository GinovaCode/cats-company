package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Compute permissions once, then use ordered, bounded history lookups per topic.
// The newest K global hits can only contain a topic's newest K hits; bounding each
// topic before the final merge preserves ordering and avoids scanning every match.
// Keep sender and content filters inside the lateral lookup, before its limit.
// CASE defers full block-schema validation until the visible fields match.
// The immutable document and trigram index prefilter rare/absent substrings;
// the predicates below remain authoritative for visibility and exact matching.
const postgresMessageSearchQuery = `
WITH readable_topics AS MATERIALIZED (
  SELECT t.id, COALESCE(g.name, t.name, '') AS topic_name
  FROM users viewer
  JOIN group_members gm ON gm.user_id = viewer.id
  JOIN "groups" g ON g.id = gm.group_id
  JOIN topics t ON t.type = 'group' AND t.id = 'grp_' || gm.group_id::text
  WHERE viewer.id = $1 AND gm.user_id IS NOT NULL
    AND (viewer.account_type <> 'human' OR COALESCE(g.group_kind, 'standard') <> 'channel_managed')
  UNION ALL
  SELECT t.id, COALESCE(NULLIF(ct.title, ''), NULLIF(peer.display_name, ''), peer.username, t.name, '')
  FROM users viewer
  JOIN users peer ON peer.id <> viewer.id
  JOIN topics t ON t.type = 'p2p'
    AND t.id = 'p2p_' || LEAST(viewer.id, peer.id)::text || '_' || GREATEST(viewer.id, peer.id)::text
  LEFT JOIN bot_config peer_bot ON peer_bot.user_id = peer.id
  LEFT JOIN conversation_titles ct ON ct.user_id = viewer.id AND ct.topic_id = t.id
  WHERE viewer.id = $1 AND peer.id IS NOT NULL AND (
    viewer.account_type = 'bot' OR peer.account_type <> 'bot' OR peer_bot.owner_id = viewer.id OR EXISTS (
      SELECT 1 FROM friends f
      WHERE f.from_user_id = viewer.id AND f.to_user_id = peer.id AND f.status = 'accepted'
    )
  )
), normalized_messages AS (
  SELECT messages.*,
         CASE
           WHEN msg_type <> 'file' OR NOT pg_input_is_valid(content, 'jsonb') THEN '{}'::jsonb
           WHEN jsonb_typeof(content::jsonb) = 'string'
             AND pg_input_is_valid(content::jsonb #>> '{}', 'jsonb')
             THEN (content::jsonb #>> '{}')::jsonb
           ELSE content::jsonb
         END AS search_legacy_content,
         CASE
           WHEN content_blocks IS NULL THEN TRUE
           WHEN jsonb_typeof(content_blocks) = 'null' THEN TRUE
           WHEN jsonb_typeof(content_blocks) <> 'array' THEN FALSE
           ELSE NOT EXISTS (
             SELECT 1
             FROM jsonb_array_elements(content_blocks) AS typed_block
             WHERE jsonb_typeof(typed_block) NOT IN ('object', 'null')
               OR (
                 jsonb_typeof(typed_block) = 'object'
                 AND (
                   EXISTS (
                     SELECT 1
                     FROM jsonb_object_keys(typed_block) AS block_key
                     WHERE lower(block_key) IN (
                       'type', 'text', 'thinking', 'payload', 'id',
                       'name', 'input', 'tool_use_id', 'content', 'is_error'
                     )
                     AND block_key NOT IN (
                       'type', 'text', 'thinking', 'payload', 'id',
                       'name', 'input', 'tool_use_id', 'content', 'is_error'
                     )
                   )
                   OR
                   (typed_block ? 'type' AND jsonb_typeof(typed_block->'type') NOT IN ('string', 'null'))
                   OR (typed_block ? 'text' AND jsonb_typeof(typed_block->'text') NOT IN ('string', 'null'))
                   OR (typed_block ? 'thinking' AND jsonb_typeof(typed_block->'thinking') NOT IN ('string', 'null'))
                   OR (typed_block ? 'payload' AND jsonb_typeof(typed_block->'payload') NOT IN ('object', 'null'))
                   OR (typed_block ? 'id' AND jsonb_typeof(typed_block->'id') NOT IN ('string', 'null'))
                   OR (typed_block ? 'name' AND jsonb_typeof(typed_block->'name') NOT IN ('string', 'null'))
                   OR (typed_block ? 'input' AND jsonb_typeof(typed_block->'input') NOT IN ('object', 'null'))
                   OR (typed_block ? 'tool_use_id' AND jsonb_typeof(typed_block->'tool_use_id') NOT IN ('string', 'null'))
                   OR (typed_block ? 'content' AND jsonb_typeof(typed_block->'content') NOT IN ('string', 'null'))
                   OR (typed_block ? 'is_error' AND jsonb_typeof(typed_block->'is_error') NOT IN ('boolean', 'null'))
                   OR (
                     jsonb_typeof(typed_block->'payload') = 'object'
                     AND (
                       (typed_block->'payload' ? 'name' AND jsonb_typeof(typed_block->'payload'->'name') NOT IN ('string', 'null'))
                       OR (typed_block->'payload' ? 'file_name' AND jsonb_typeof(typed_block->'payload'->'file_name') NOT IN ('string', 'null'))
                       OR (typed_block->'payload' ? 'filename' AND jsonb_typeof(typed_block->'payload'->'filename') NOT IN ('string', 'null'))
                       OR (typed_block->'payload' ? 'title' AND jsonb_typeof(typed_block->'payload'->'title') NOT IN ('string', 'null'))
                     )
                   )
                 )
               )
           )
         END AS search_blocks_valid
  FROM messages
)
SELECT m.id, m.topic_id, readable.topic_name,
       m.from_uid, m.sender_name, m.content, m.msg_type, m.created_at, m.content_blocks
FROM readable_topics readable
CROSS JOIN LATERAL (
SELECT m.id, m.topic_id,
       m.from_uid, COALESCE(NULLIF(sender.display_name, ''), sender.username, '') AS sender_name,
       m.content, m.msg_type, m.created_at, m.content_blocks
FROM normalized_messages m
JOIN users sender ON sender.id = m.from_uid
WHERE m.topic_id = readable.id AND sender.account_type IN ('human', 'bot')
AND message_search_document_v1(m.msg_type, m.content, m.content_blocks)
    LIKE '%' || replace(replace(replace(lower($3), '!', '!!'), '%', '!%'), '_', '!_') || '%' ESCAPE '!'
AND CASE WHEN (
  ($2 <> 'artifact' AND m.msg_type = 'text'
    AND (
      EXISTS (
        SELECT 1 FROM jsonb_array_elements(
          CASE WHEN jsonb_typeof(m.content_blocks) = 'array' THEN m.content_blocks ELSE '[]'::jsonb END
        ) AS block
        WHERE block->>'type' IN ('text', 'assistant_text')
          AND STRPOS(LOWER(COALESCE(NULLIF(block->>'text', ''), block->>'content', '')), LOWER($3)) > 0
      )
      OR (
        STRPOS(LOWER(m.content), LOWER($3)) > 0
        AND NOT EXISTS (
          SELECT 1 FROM jsonb_array_elements(
            CASE WHEN jsonb_typeof(m.content_blocks) = 'array' THEN m.content_blocks ELSE '[]'::jsonb END
          ) AS block
          WHERE block->>'type' IN ('thinking', 'tool_use', 'tool_result', 'runtime_plan', 'debug')
            OR (block->>'type' IN ('text', 'assistant_text')
              AND COALESCE(NULLIF(block->>'text', ''), block->>'content', '') <> '')
        )
      )
    ))
  OR
  ($2 <> 'message' AND (
    (m.content_blocks IS NOT NULL AND jsonb_typeof(m.content_blocks) = 'array'
      AND EXISTS (
      SELECT 1 FROM jsonb_array_elements(
        CASE WHEN jsonb_typeof(m.content_blocks) = 'array' THEN m.content_blocks ELSE '[]'::jsonb END
      ) AS artifact
      WHERE artifact->>'type' IN ('file', 'image', 'audio', 'video')
        AND (
          STRPOS(LOWER(COALESCE(artifact->>'name', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'name', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'file_name', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'filename', '')), LOWER($3)) > 0
          OR STRPOS(LOWER(COALESCE(artifact->'payload'->>'title', '')), LOWER($3)) > 0
        )
    ))
    OR (m.msg_type = 'file' AND EXISTS (
      SELECT 1
      FROM LATERAL (
        SELECT CASE
          WHEN jsonb_typeof(m.search_legacy_content->'payload') = 'object'
            THEN m.search_legacy_content->'payload'
          ELSE m.search_legacy_content
        END AS content
      ) AS legacy_file
      WHERE (jsonb_typeof(legacy_file.content->'name') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'name'), LOWER($3)) > 0)
        OR (jsonb_typeof(legacy_file.content->'file_name') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'file_name'), LOWER($3)) > 0)
        OR (jsonb_typeof(legacy_file.content->'filename') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'filename'), LOWER($3)) > 0)
        OR (jsonb_typeof(legacy_file.content->'title') = 'string'
          AND STRPOS(LOWER(legacy_file.content->>'title'), LOWER($3)) > 0)
    ))
  ))
) THEN m.search_blocks_valid ELSE FALSE END
ORDER BY m.created_at DESC, m.id DESC
LIMIT ($4::int + $5::int)
) m
ORDER BY m.created_at DESC, m.id DESC
LIMIT $4 OFFSET $5`

// Force a value-specific plan inside the search transaction. A generic plan
// cannot estimate substring selectivity and may scan/sort all common-term hits.
const postgresMessageSearchPlanMode = `SET LOCAL plan_cache_mode = force_custom_plan`

// SearchMessages searches only topics readable by viewerUID. Access filtering is atomic with selection.
func (a *Adapter) SearchMessages(ctx context.Context, viewerUID int64, query, searchType string, limit int) ([]*store.MessageSearchResult, error) {
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin message search: %w", err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, postgresMessageSearchPlanMode); err != nil {
		return nil, fmt.Errorf("plan message search: %w", err)
	}
	results, err := store.CollectMessageSearchResults(limit, func(pageSize, offset, remaining int) ([]*store.MessageSearchResult, int, error) {
		rows, err := tx.QueryContext(ctx, postgresMessageSearchQuery, viewerUID, searchType, query, pageSize, offset)
		if err != nil {
			return nil, 0, fmt.Errorf("search messages: %w", err)
		}
		page, scanned, scanErr := scanPostgresMessageSearch(rows, query, searchType, remaining)
		closeErr := rows.Close()
		if scanErr != nil {
			return nil, scanned, scanErr
		}
		if closeErr != nil {
			return nil, scanned, fmt.Errorf("close message search rows: %w", closeErr)
		}
		return page, scanned, nil
	})
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish message search: %w", err)
	}
	return results, nil
}

func scanPostgresMessageSearch(rows *sql.Rows, query, searchType string, limit int) ([]*store.MessageSearchResult, int, error) {
	results := make([]*store.MessageSearchResult, 0, limit)
	scanned := 0
	for rows.Next() {
		scanned++
		var (
			result     store.MessageSearchResult
			msgType    string
			blocksJSON interface{}
		)
		if err := rows.Scan(&result.MessageID, &result.TopicID, &result.TopicName, &result.FromUID,
			&result.SenderName, &result.Content, &msgType, &result.CreatedAt, &blocksJSON); err != nil {
			return nil, scanned, fmt.Errorf("scan message search result: %w", err)
		}
		blocks := jsonBytes(blocksJSON)
		match, ok := store.MatchMessageSearchCandidate(store.MessageSearchCandidate{
			Result:        result,
			MessageType:   msgType,
			ContentBlocks: blocks,
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

func jsonBytes(value interface{}) []byte {
	switch typed := value.(type) {
	case nil:
		return nil
	case []byte:
		return typed
	case string:
		return []byte(typed)
	default:
		raw, _ := json.Marshal(typed)
		return raw
	}
}

// GetMessagesAround returns a bounded chronological window around a message in one topic.
func (a *Adapter) GetMessagesAround(topicID string, messageID int64, limit int) ([]*types.Message, error) {
	beforeLimit := (limit + 1) / 2
	afterLimit := limit - beforeLimit
	rows, err := a.db.Query(`
SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to FROM (
  (SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to
   FROM messages WHERE topic_id = $1 AND id <= $2 ORDER BY id DESC LIMIT $3)
  UNION ALL
  (SELECT id, topic_id, from_uid, content, msg_type, created_at, content_blocks, mode, role, metadata, COALESCE(reply_to, 0) AS reply_to
   FROM messages WHERE topic_id = $1 AND id > $2 ORDER BY id ASC LIMIT $4)
) around_messages ORDER BY id ASC`, topicID, messageID, beforeLimit, afterLimit)
	if err != nil {
		return nil, fmt.Errorf("get messages around: %w", err)
	}
	defer rows.Close()
	return scanMessages(rows, "scan message around")
}
