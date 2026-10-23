CREATE TABLE IF NOT EXISTS skillhub_publication_description_applications (
    entry_id VARCHAR(41) NOT NULL,
    round_number INTEGER NOT NULL CHECK (round_number BETWEEN 1 AND 3),
    confirmation_key CHAR(64) NOT NULL CHECK (confirmation_key ~ '^[a-f0-9]{64}$'),
    application_id VARCHAR(72) NOT NULL UNIQUE CHECK (application_id ~ '^pubdesc_[a-f0-9]{64}$'),
    input JSONB NOT NULL CHECK (jsonb_typeof(input)='object' AND octet_length(input::text)<=98304),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    result JSONB CHECK (jsonb_typeof(result)='object' AND octet_length(result::text)<=4096),
    applied_at TIMESTAMPTZ,
    PRIMARY KEY(entry_id,round_number),
    FOREIGN KEY(entry_id,round_number) REFERENCES skillhub_publication_description_confirmations(entry_id,round_number) ON DELETE RESTRICT,
    CHECK ((result IS NULL)=(applied_at IS NULL)),
    CHECK (applied_at IS NULL OR applied_at>=created_at)
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description_application() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    e RECORD;
    t RECORD;
    r RECORD;
    d RECORD;
    c RECORD;
    g RECORD;
    found_uid BIGINT;
    task_topic TEXT;
    task_group BIGINT;
    member_count INTEGER;
    digest TEXT;
BEGIN
    IF TG_OP='DELETE' THEN
        RAISE EXCEPTION 'retained publication description application is immutable';
    END IF;
    IF TG_OP='UPDATE' AND ((to_jsonb(NEW)-'result'-'applied_at') IS DISTINCT FROM (to_jsonb(OLD)-'result'-'applied_at')
        OR OLD.result IS NOT NULL OR NEW.result IS NULL OR NEW.applied_at IS NULL) THEN
        RAISE EXCEPTION 'publication description application may only complete once';
    END IF;
    IF TG_OP='INSERT' AND (NEW.result IS NOT NULL OR NEW.applied_at IS NOT NULL) THEN
        RAISE EXCEPTION 'publication description application must be prepared first';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR e.state IS DISTINCT FROM 'waiting_runtime' OR e.entrypoint IS DISTINCT FROM 'workspace' THEN
        RAISE EXCEPTION 'publication description application entry is unavailable';
    END IF;
    SELECT id INTO found_uid FROM users WHERE id=e.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication description application actor is inactive';
    END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id
        AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND OR EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication description application authority changed or entry settled';
    END IF;
    SELECT * INTO t FROM skillhub_publication_repair_tasks WHERE entry_id=NEW.entry_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication description application task is missing';
    END IF;
    task_topic := t.input->>'topicId';
    IF task_topic IS NULL OR task_topic !~ '^grp_[1-9][0-9]{0,18}$' THEN
        RAISE EXCEPTION 'publication description application topic is invalid';
    END IF;
    task_group := substring(task_topic FROM 5)::BIGINT;
    SELECT * INTO g FROM "groups" WHERE id=task_group FOR UPDATE;
    IF NOT FOUND OR g.owner_id IS DISTINCT FROM e.actor_uid OR g.group_kind IS DISTINCT FROM 'agent_task' THEN
        RAISE EXCEPTION 'publication description application task authority changed';
    END IF;
    SELECT owner_id INTO found_uid FROM topics WHERE id=task_topic AND type='group' FOR SHARE;
    IF NOT FOUND OR found_uid IS DISTINCT FROM e.actor_uid THEN
        RAISE EXCEPTION 'publication description application topic authority changed';
    END IF;
    PERFORM user_id FROM group_members WHERE group_id=task_group ORDER BY user_id FOR SHARE;
    SELECT count(*) INTO member_count FROM group_members WHERE group_id=task_group;
    IF member_count<>2 OR NOT EXISTS(SELECT 1 FROM group_members WHERE group_id=task_group AND user_id=e.actor_uid AND role='owner')
        OR NOT EXISTS(SELECT 1 FROM group_members WHERE group_id=task_group AND user_id=e.bot_uid) THEN
        RAISE EXCEPTION 'publication description application membership changed';
    END IF;
    SELECT * INTO r FROM skillhub_publication_repair_rounds WHERE entry_id=NEW.entry_id AND round_number=NEW.round_number;
    IF NOT FOUND OR NEW.round_number<>(SELECT count(*) FROM skillhub_publication_repair_rounds WHERE entry_id=NEW.entry_id)
        OR r.result->>'outcome' IS DISTINCT FROM 'waiting_confirmation' THEN
        RAISE EXCEPTION 'publication description application round is not waiting for confirmation';
    END IF;
    SELECT * INTO d FROM skillhub_publication_description_drafts WHERE entry_id=NEW.entry_id AND round_number=NEW.round_number;
    IF NOT FOUND OR NEW.input->'draft' IS DISTINCT FROM d.input OR NEW.confirmation_key IS DISTINCT FROM d.confirmation_key
        OR NEW.input->>'confirmationKey' IS DISTINCT FROM d.confirmation_key::TEXT
        OR NEW.input->'receipt' IS DISTINCT FROM t.input->'receipt'
        OR r.result->'evidence' IS DISTINCT FROM d.input->'evidence'
        OR NEW.application_id IS DISTINCT FROM 'pubdesc_'||NEW.identity_hash THEN
        RAISE EXCEPTION 'publication description application exact binding is invalid';
    END IF;
    SELECT * INTO c FROM skillhub_publication_description_confirmations WHERE entry_id=NEW.entry_id AND round_number=NEW.round_number;
    IF NOT FOUND OR c.actor_uid IS DISTINCT FROM e.actor_uid OR c.confirmation_key IS DISTINCT FROM NEW.confirmation_key
        OR NEW.created_at<c.confirmed_at THEN
        RAISE EXCEPTION 'publication description application is not confirmed';
    END IF;
    IF NEW.result IS NOT NULL THEN
        IF NEW.result->>'applicationId' IS DISTINCT FROM NEW.application_id
            OR jsonb_typeof(NEW.result->'requestBytes') IS DISTINCT FROM 'number'
            OR (NEW.result->>'requestBytes') !~ '^[1-9][0-9]{0,8}$'
            OR (NEW.result->>'requestBytes')::NUMERIC>78643200
            OR NEW.result->'derivation'->>'schemaVersion' IS DISTINCT FROM '1'
            OR NEW.result->'derivation'->>'algorithm' IS DISTINCT FROM 'skillhub-description-derivation-v1'
            OR NEW.result->'derivation'->>'originalRequestHash' IS DISTINCT FROM NEW.input->'receipt'->'Query'->>'RequestHash'
            OR NEW.result->'derivation'->>'originalCandidateHash' IS DISTINCT FROM NEW.input->'draft'->'evidence'->>'candidateHash'
            OR NEW.result->'derivation'->>'derivedRequestHash' IS NOT DISTINCT FROM NEW.result->'derivation'->>'originalRequestHash'
            OR NEW.result->'derivation'->>'derivedCandidateHash' IS NOT DISTINCT FROM NEW.result->'derivation'->>'originalCandidateHash' THEN
            RAISE EXCEPTION 'publication description application result binding is invalid';
        END IF;
        FOREACH digest IN ARRAY ARRAY['originalRequestHash','originalCandidateHash','validationDigest','proposalDigest','limitsFingerprint','derivedRequestHash','derivedCandidateHash'] LOOP
            IF (NEW.result->'derivation'->>digest ~ '^[a-f0-9]{64}$') IS NOT TRUE THEN
                RAISE EXCEPTION 'publication description application digest is invalid';
            END IF;
        END LOOP;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_application ON skillhub_publication_description_applications;
CREATE TRIGGER guard_skillhub_publication_description_application BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_applications
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description_application();
