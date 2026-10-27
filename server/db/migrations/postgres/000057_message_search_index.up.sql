CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_search_document_trgm
  ON messages USING gin (message_search_document_v1(msg_type, content, content_blocks) public.gin_trgm_ops);
