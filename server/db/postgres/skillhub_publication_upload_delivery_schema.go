package postgres

// Mirror migration 000048. Upload attempts are retained separately from
// capture leases so a runtime ACK cannot be mistaken for Cloud acceptance.
const createSkillHubPublicationUploadDeliveriesTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_upload_deliveries (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_entries(entry_id),
    state TEXT NOT NULL CHECK (state IN ('leased','pending','uploaded','needs_attention')),
    attempts INTEGER NOT NULL CHECK (attempts BETWEEN 0 AND 8),
    lease_token VARCHAR(32) NOT NULL CHECK (lease_token ~ '^[a-f0-9]{32}$'),
    lease_node VARCHAR(128) NOT NULL CHECK (lease_node ~ '^[A-Za-z0-9._:-]{1,128}$'),
    lease_connection VARCHAR(128) NOT NULL CHECK (lease_connection ~ '^[A-Za-z0-9._:-]{1,128}$'),
    lease_generation BIGINT NOT NULL CHECK (lease_generation > 0),
    lease_until TIMESTAMPTZ NOT NULL,
    next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    last_code TEXT NOT NULL DEFAULT '' CHECK (last_code IN ('','retry','rejected','exhausted')),
    CHECK ((state IN ('leased','uploaded') AND last_code='') OR (state='pending' AND last_code IN ('','retry')) OR (state='needs_attention' AND last_code IN ('rejected','exhausted')))
);
CREATE INDEX IF NOT EXISTS idx_skillhub_publication_upload_delivery_due ON skillhub_publication_upload_deliveries(state,next_attempt_at,entry_id);
-- Captures may predate this queue. Reconcile them into the durable upload lane
-- without inferring Cloud acceptance when a receipt already exists.
INSERT INTO skillhub_publication_upload_deliveries
	(entry_id,state,attempts,lease_token,lease_node,lease_connection,lease_generation,lease_until,next_attempt_at,last_code)
SELECT c.entry_id,'pending',0,repeat('0',32),'pending','pending',1,clock_timestamp(),clock_timestamp(),''
FROM skillhub_publication_captures c
LEFT JOIN skillhub_publication_cloud_receipts r ON r.entry_id=c.entry_id
WHERE r.entry_id IS NULL
ON CONFLICT (entry_id) DO NOTHING;
`
