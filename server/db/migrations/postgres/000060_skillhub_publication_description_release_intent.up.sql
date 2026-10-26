CREATE TABLE IF NOT EXISTS skillhub_publication_description_release_intents (
    application_id VARCHAR(72) PRIMARY KEY REFERENCES skillhub_publication_description_applications(application_id) ON DELETE RESTRICT,
    entry_id VARCHAR(41) NOT NULL UNIQUE REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    actor_uid BIGINT NOT NULL CHECK (actor_uid > 0),
    acceptance_identity_hash CHAR(64) NOT NULL CHECK (acceptance_identity_hash ~ '^[a-f0-9]{64}$'),
    target JSONB NOT NULL CHECK (jsonb_typeof(target)='object' AND octet_length(target::text)<=4096),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    requested_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description_release_intent() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    a RECORD;
    e RECORD;
    found_uid BIGINT;
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'retained publication description release intent is immutable';
    END IF;
    SELECT * INTO a FROM skillhub_publication_description_applications WHERE application_id=NEW.application_id;
    IF NOT FOUND OR a.result IS NULL OR a.entry_id IS DISTINCT FROM NEW.entry_id
        OR a.result->'derivation'->>'derivedCandidateHash' IS DISTINCT FROM NEW.target->>'candidateHash'
        OR jsonb_typeof(NEW.target) IS DISTINCT FROM 'object'
        OR (SELECT count(*) FROM jsonb_object_keys(NEW.target))<>10
        OR NOT (NEW.target ?& ARRAY['candidateHash','manifestHash','publisherUserId','publisherProfileId','namespace','skillId','version','uploadId','releaseAt','confirmVersionPublish'])
        OR NEW.target->>'confirmVersionPublish' NOT IN ('true','false') THEN
        RAISE EXCEPTION 'publication description release target binding is invalid';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR e.actor_uid IS DISTINCT FROM NEW.actor_uid OR e.state IS DISTINCT FROM 'waiting_runtime'
        OR e.entrypoint IS DISTINCT FROM 'workspace' THEN
        RAISE EXCEPTION 'publication description release entry is unavailable';
    END IF;
    SELECT id INTO found_uid FROM users WHERE id=NEW.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication description release actor is inactive';
    END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=e.bot_uid AND b.owner_id=NEW.actor_uid AND b.body_id=e.body_id
        AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND OR EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication description release authority changed or entry settled';
    END IF;
    IF NOT EXISTS(SELECT 1 FROM skillhub_publication_description_acceptances x
        WHERE x.application_id=NEW.application_id AND x.identity_hash=NEW.acceptance_identity_hash
        AND x.report->>'candidateHash'=NEW.target->>'candidateHash' AND x.report->>'outcome'='passed'
        AND x.report->>'coverage'='complete' AND x.report->'truncated'='false'::JSONB) THEN
        RAISE EXCEPTION 'publication description release requires its exact passing acceptance';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_release_intent ON skillhub_publication_description_release_intents;
CREATE TRIGGER guard_skillhub_publication_description_release_intent BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_release_intents
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description_release_intent();
