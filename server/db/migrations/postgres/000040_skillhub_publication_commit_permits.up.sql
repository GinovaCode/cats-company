CREATE TABLE IF NOT EXISTS skillhub_publication_commit_intents (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_cloud_receipts(entry_id) ON DELETE RESTRICT,
    target JSONB NOT NULL CHECK (jsonb_typeof(target)='object' AND octet_length(target::text) <= 4096),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS skillhub_publication_commit_permits (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_commit_intents(entry_id) ON DELETE RESTRICT,
    permit_id VARCHAR(42) NOT NULL UNIQUE CHECK (permit_id ~ '^pubpermit_[a-f0-9]{32}$'),
    authority_epoch INTEGER NOT NULL CHECK (authority_epoch BETWEEN 1 AND 8),
    authorized_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_commit_authority() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    entry_record RECORD;
    live_epoch INTEGER;
    found_uid BIGINT;
BEGIN
    IF TG_OP <> 'INSERT' THEN
        RAISE EXCEPTION 'retained publication commit evidence is immutable';
    END IF;
    SELECT * INTO entry_record FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR entry_record.state IS DISTINCT FROM 'waiting_runtime' THEN
        RAISE EXCEPTION 'publication commit authority is settled';
    END IF;
    SELECT id INTO found_uid FROM users
        WHERE id=entry_record.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication commit actor is inactive';
    END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=entry_record.bot_uid AND b.owner_id=entry_record.actor_uid
        AND b.body_id=entry_record.body_id AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication commit Bot authority changed';
    END IF;
    IF EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication commit was cancelled';
    END IF;
    IF TG_TABLE_NAME='skillhub_publication_commit_permits' THEN
        SELECT authority_epoch INTO live_epoch FROM skillhub_publication_upload_grants WHERE entry_id=NEW.entry_id;
        IF NEW.authority_epoch IS DISTINCT FROM live_epoch THEN
            RAISE EXCEPTION 'publication commit epoch does not match';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_commit_intent ON skillhub_publication_commit_intents;
CREATE TRIGGER guard_skillhub_publication_commit_intent BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_commit_intents
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_commit_authority();
DROP TRIGGER IF EXISTS guard_skillhub_publication_commit_permit ON skillhub_publication_commit_permits;
CREATE TRIGGER guard_skillhub_publication_commit_permit BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_commit_permits
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_commit_authority();
