CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_search_grams_gin
  ON messages USING gin (message_search_grams_v1(message_search_document_v1(msg_type, content, content_blocks)));
