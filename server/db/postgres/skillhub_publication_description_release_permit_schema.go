package postgres

// Mirrors migration 000061. The permit is a separate, exact derived-target
// authorization and never grants the legacy receipt commit path.
const createSkillHubPublicationDescriptionReleasePermitTable = `
CREATE TABLE IF NOT EXISTS skillhub_publication_description_release_permits (
    application_id VARCHAR(72) PRIMARY KEY REFERENCES skillhub_publication_description_release_intents(application_id) ON DELETE RESTRICT,
    entry_id VARCHAR(41) NOT NULL UNIQUE REFERENCES skillhub_publication_entries(entry_id) ON DELETE RESTRICT,
    permit_id VARCHAR(46) NOT NULL UNIQUE CHECK (permit_id ~ '^pubdescpermit_[a-f0-9]{32}$'),
    intent_identity_hash CHAR(64) NOT NULL CHECK (intent_identity_hash ~ '^[a-f0-9]{64}$'),
    authorized_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description_release_permit() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    i RECORD;
    e RECORD;
    found_uid BIGINT;
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'retained publication description release permits are immutable';
    END IF;
    SELECT * INTO i FROM skillhub_publication_description_release_intents WHERE application_id=NEW.application_id;
    IF NOT FOUND OR i.entry_id IS DISTINCT FROM NEW.entry_id OR i.identity_hash IS DISTINCT FROM NEW.intent_identity_hash THEN
        RAISE EXCEPTION 'description release permit does not match retained human intent';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR e.actor_uid IS DISTINCT FROM i.actor_uid OR e.state IS DISTINCT FROM 'waiting_runtime'
        OR e.entrypoint IS DISTINCT FROM 'workspace' THEN
        RAISE EXCEPTION 'description release permit entry is unavailable';
    END IF;
    SELECT id INTO found_uid FROM users WHERE id=i.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN RAISE EXCEPTION 'description release permit actor is inactive'; END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=e.bot_uid AND b.owner_id=i.actor_uid AND b.body_id=e.body_id
        AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND OR EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'description release authority changed or entry settled';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_release_permit ON skillhub_publication_description_release_permits;
CREATE TRIGGER guard_skillhub_publication_description_release_permit BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_release_permits
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description_release_permit();
CREATE OR REPLACE FUNCTION guard_skillhub_publication_no_legacy_commit_after_description_release() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF EXISTS(SELECT 1 FROM skillhub_publication_description_release_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'description release intent cannot enter legacy publication commit';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_no_legacy_commit_after_description_release ON skillhub_publication_commit_intents;
CREATE TRIGGER guard_skillhub_publication_no_legacy_commit_after_description_release BEFORE INSERT ON skillhub_publication_commit_intents
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_no_legacy_commit_after_description_release();
CREATE OR REPLACE FUNCTION guard_skillhub_publication_release_started_cancellation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF TG_TABLE_NAME='skillhub_publication_entries' THEN
        IF NEW.state IS DISTINCT FROM OLD.state AND NEW.state='cancelled'
            AND EXISTS(SELECT 1 FROM skillhub_publication_description_release_permits WHERE entry_id=OLD.entry_id) THEN
            RAISE EXCEPTION 'publication release permit already issued';
        END IF;
        RETURN NEW;
    END IF;
    PERFORM 1 FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND THEN
        RETURN NEW;
    END IF;
    IF EXISTS(SELECT 1 FROM skillhub_publication_description_release_permits WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication release permit already issued';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_release_started_entry_cancel ON skillhub_publication_entries;
CREATE TRIGGER guard_skillhub_publication_release_started_entry_cancel BEFORE UPDATE OF state ON skillhub_publication_entries
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_release_started_cancellation();
DROP TRIGGER IF EXISTS guard_skillhub_publication_release_started_cancel_intent ON skillhub_publication_cancel_intents;
CREATE TRIGGER guard_skillhub_publication_release_started_cancel_intent BEFORE INSERT ON skillhub_publication_cancel_intents
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_release_started_cancellation();
`
