package postgres

// Mirror migration 000051. This ledger has no registered execution adapter.
const createSkillHubPublicationRepairTables = `
CREATE TABLE IF NOT EXISTS skillhub_publication_repair_tasks (
    entry_id VARCHAR(41) PRIMARY KEY REFERENCES skillhub_publication_cloud_receipts(entry_id) ON DELETE RESTRICT,
    task_id VARCHAR(42) NOT NULL UNIQUE CHECK (task_id ~ '^pubrepair_[a-f0-9]{32}$'),
    input JSONB NOT NULL CHECK (jsonb_typeof(input)='object' AND octet_length(input::text)<=8192),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE TABLE IF NOT EXISTS skillhub_publication_repair_rounds (
    entry_id VARCHAR(41) NOT NULL REFERENCES skillhub_publication_repair_tasks(entry_id) ON DELETE RESTRICT,
    round_number INTEGER NOT NULL CHECK (round_number BETWEEN 1 AND 3),
    request_id VARCHAR(128) NOT NULL CHECK (request_id ~ '^[A-Za-z0-9_-]{8,128}$'),
    input JSONB NOT NULL CHECK (jsonb_typeof(input)='object' AND octet_length(input::text)<=1024
        AND input->>'requestId' IS NOT NULL AND input->>'requestId'=request_id),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    lease_worker VARCHAR(128) NOT NULL CHECK (lease_worker ~ '^[A-Za-z0-9._:-]{1,128}$'),
    lease_token CHAR(32) NOT NULL CHECK (lease_token ~ '^[a-f0-9]{32}$'),
    lease_generation BIGINT NOT NULL CHECK (lease_generation > 0),
    lease_until TIMESTAMPTZ NOT NULL,
    result JSONB CHECK (result IS NULL OR (jsonb_typeof(result)='object' AND octet_length(result::text)<=1024
        AND result->>'outcome' IN ('rejected','validated','waiting_confirmation','stopped'))),
    PRIMARY KEY(entry_id,round_number),
    UNIQUE(entry_id,request_id)
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_repair() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    e RECORD;
    p RECORD;
    prior_count INTEGER;
    first_input JSONB;
    found_uid BIGINT;
BEGIN
    IF TG_OP='DELETE' OR (TG_TABLE_NAME='skillhub_publication_repair_tasks' AND TG_OP<>'INSERT') THEN
        RAISE EXCEPTION 'retained publication repair evidence cannot be erased or replaced';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR e.state IS DISTINCT FROM 'waiting_runtime' THEN
        RAISE EXCEPTION 'publication repair entry is settled';
    END IF;
    SELECT id INTO found_uid FROM users WHERE id=e.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication repair actor is inactive';
    END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id
        AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication repair Bot authority changed';
    END IF;
    IF EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication repair is cancelled or committing';
    END IF;
    IF TG_TABLE_NAME='skillhub_publication_repair_tasks' THEN
        RETURN NEW;
    END IF;
    IF TG_OP='INSERT' THEN
        SELECT count(*) INTO prior_count FROM skillhub_publication_repair_rounds WHERE entry_id=NEW.entry_id;
        IF NEW.round_number<>prior_count+1 OR NEW.lease_generation<>1 OR NEW.result IS NOT NULL THEN
            RAISE EXCEPTION 'publication repair round cannot skip or reset budget';
        END IF;
        IF prior_count=0 THEN
            SELECT input INTO first_input FROM skillhub_publication_repair_tasks WHERE entry_id=NEW.entry_id;
            IF NEW.input->'evidence' IS DISTINCT FROM first_input->'evidence' THEN
                RAISE EXCEPTION 'publication repair initial evidence changed';
            END IF;
        ELSE
            SELECT * INTO p FROM skillhub_publication_repair_rounds
                WHERE entry_id=NEW.entry_id AND round_number=prior_count;
            IF p.result IS NULL OR p.result->>'outcome' IS DISTINCT FROM 'rejected'
                OR (p.result->'evidence'->>'candidateHash'=p.input->'evidence'->>'candidateHash'
                    AND p.result->'evidence'->>'issueSetHash'=p.input->'evidence'->>'issueSetHash')
                OR NEW.input->'evidence' IS DISTINCT FROM p.result->'evidence' THEN
                RAISE EXCEPTION 'publication repair cannot advance without progress';
            END IF;
        END IF;
    ELSE
        IF NEW.entry_id IS DISTINCT FROM OLD.entry_id OR NEW.round_number IS DISTINCT FROM OLD.round_number
            OR NEW.request_id IS DISTINCT FROM OLD.request_id OR NEW.input IS DISTINCT FROM OLD.input
            OR NEW.identity_hash IS DISTINCT FROM OLD.identity_hash OR OLD.result IS NOT NULL THEN
            RAISE EXCEPTION 'publication repair round identity/checkpoint is immutable';
        END IF;
        IF NEW.result IS NOT NULL THEN
            IF NEW.lease_generation<>OLD.lease_generation OR NEW.lease_token<>OLD.lease_token
                OR NEW.lease_worker<>OLD.lease_worker OR NEW.lease_until<>OLD.lease_until
                OR OLD.lease_until<=clock_timestamp() THEN
                RAISE EXCEPTION 'publication repair checkpoint has stale lease';
            END IF;
        ELSIF OLD.lease_until>clock_timestamp() OR NEW.lease_generation<>OLD.lease_generation+1
            OR NEW.lease_token=OLD.lease_token OR NEW.lease_until<=clock_timestamp() THEN
            RAISE EXCEPTION 'publication repair lease cannot be reset';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_repair_task ON skillhub_publication_repair_tasks;
CREATE TRIGGER guard_skillhub_publication_repair_task BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_repair_tasks
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_repair();
DROP TRIGGER IF EXISTS guard_skillhub_publication_repair_round ON skillhub_publication_repair_rounds;
CREATE TRIGGER guard_skillhub_publication_repair_round BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_repair_rounds
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_repair();
`
