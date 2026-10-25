CREATE OR REPLACE FUNCTION message_search_grams_v1(document text)
RETURNS integer[] LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
  WITH document_bytes AS MATERIALIZED (SELECT convert_to(document, 'UTF8') AS value)
  SELECT ARRAY(
    SELECT DISTINCT (get_byte(value, i) << 16) | (get_byte(value, i+1) << 8) | get_byte(value, i+2)
    FROM document_bytes, LATERAL generate_series(0, octet_length(value)-3) AS i
    ORDER BY 1
  );
$$;
