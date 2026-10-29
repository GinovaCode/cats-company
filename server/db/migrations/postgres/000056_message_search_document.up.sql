CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;
CREATE OR REPLACE FUNCTION message_search_document_v1(kind text, body text, blocks jsonb)
RETURNS text LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
  fields text[] := ARRAY[]::text[];
  block jsonb;
  visible text;
  has_visible boolean := false;
  has_internal boolean := false;
  legacy jsonb;
BEGIN
  IF jsonb_typeof(blocks) = 'array' THEN
    FOR block IN SELECT value FROM jsonb_array_elements(blocks) LOOP
      IF block->>'type' IN ('text', 'assistant_text') AND kind = 'text' THEN
        visible := COALESCE(NULLIF(block->>'text', ''), block->>'content', '');
        IF visible <> '' THEN
          fields := array_append(fields, visible);
          has_visible := true;
        END IF;
      ELSIF block->>'type' IN ('thinking', 'tool_use', 'tool_result', 'runtime_plan', 'debug') THEN
        has_internal := true;
      ELSIF block->>'type' IN ('file', 'image', 'audio', 'video') THEN
        fields := fields || ARRAY[block->>'name', block->'payload'->>'name',
          block->'payload'->>'file_name', block->'payload'->>'filename', block->'payload'->>'title'];
      END IF;
    END LOOP;
  END IF;
  IF kind = 'text' AND NOT has_visible AND NOT has_internal THEN
    fields := array_append(fields, body);
  ELSIF kind = 'file' AND pg_input_is_valid(body, 'jsonb') THEN
    legacy := body::jsonb;
    IF jsonb_typeof(legacy) = 'string' AND pg_input_is_valid(legacy #>> '{}', 'jsonb') THEN
      legacy := (legacy #>> '{}')::jsonb;
    END IF;
    IF jsonb_typeof(legacy->'payload') = 'object' THEN
      legacy := legacy->'payload';
    END IF;
    fields := fields || ARRAY[legacy->>'name', legacy->>'file_name', legacy->>'filename', legacy->>'title'];
  END IF;
  RETURN lower(array_to_string(fields, E'\n'));
END;
$$;
