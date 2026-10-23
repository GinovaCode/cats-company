package postgres

// Mirror migration 000052. No executor or candidate writer is registered.
const createSkillHubPublicationDescriptionTables = `
CREATE TABLE IF NOT EXISTS skillhub_publication_description_drafts (
    entry_id VARCHAR(41) NOT NULL,
    round_number INTEGER NOT NULL CHECK (round_number BETWEEN 1 AND 3),
    input JSONB NOT NULL CHECK (jsonb_typeof(input)='object' AND octet_length(input::text)<=65536),
    confirmation_key CHAR(64) NOT NULL CHECK (confirmation_key ~ '^[a-f0-9]{64}$'),
    lease_worker VARCHAR(128) NOT NULL,
    lease_token CHAR(32) NOT NULL,
    lease_generation BIGINT NOT NULL CHECK (lease_generation>0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(entry_id,round_number),
    UNIQUE(entry_id,round_number,confirmation_key),
    FOREIGN KEY(entry_id,round_number) REFERENCES skillhub_publication_repair_rounds(entry_id,round_number) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS skillhub_publication_description_confirmations (
    entry_id VARCHAR(41) NOT NULL,
    round_number INTEGER NOT NULL,
    confirmation_key CHAR(64) NOT NULL,
    actor_uid BIGINT NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    confirmed_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
    PRIMARY KEY(entry_id,round_number),
    FOREIGN KEY(entry_id,round_number,confirmation_key)
        REFERENCES skillhub_publication_description_drafts(entry_id,round_number,confirmation_key) ON DELETE RESTRICT
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    e RECORD;
    t RECORD;
    r RECORD;
    d RECORD;
    g RECORD;
    found_uid BIGINT;
    task_topic TEXT;
    task_group BIGINT;
    member_count INTEGER;
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'retained publication description evidence is immutable';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=NEW.entry_id FOR UPDATE;
    IF NOT FOUND OR e.state IS DISTINCT FROM 'waiting_runtime' OR e.entrypoint IS DISTINCT FROM 'workspace' THEN
        RAISE EXCEPTION 'publication description entry is unavailable';
    END IF;
    SELECT id INTO found_uid FROM users WHERE id=e.actor_uid AND account_type='human' AND state=0 FOR SHARE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication description actor is inactive';
    END IF;
    SELECT b.user_id INTO found_uid FROM bot_config b JOIN users u ON u.id=b.user_id
        WHERE b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id
        AND b.enabled=true AND u.account_type='bot' AND u.state=0 FOR SHARE OF b,u;
    IF NOT FOUND OR EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=NEW.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication description authority changed or entry settled';
    END IF;
    SELECT * INTO t FROM skillhub_publication_repair_tasks WHERE entry_id=NEW.entry_id;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'publication description task is missing';
    END IF;
    task_topic := t.input->>'topicId';
    IF task_topic IS NULL OR task_topic !~ '^grp_[1-9][0-9]{0,18}$' THEN
        RAISE EXCEPTION 'publication description task topic is invalid';
    END IF;
    task_group := substring(task_topic FROM 5)::BIGINT;
    SELECT * INTO g FROM "groups" WHERE id=task_group FOR UPDATE;
    IF NOT FOUND OR g.owner_id IS DISTINCT FROM e.actor_uid OR g.group_kind IS DISTINCT FROM 'agent_task' THEN
        RAISE EXCEPTION 'publication description task authority changed';
    END IF;
    SELECT owner_id INTO found_uid FROM topics WHERE id=task_topic AND type='group' FOR SHARE;
    IF NOT FOUND OR found_uid IS DISTINCT FROM e.actor_uid THEN
        RAISE EXCEPTION 'publication description topic authority changed';
    END IF;
    PERFORM user_id FROM group_members WHERE group_id=task_group ORDER BY user_id FOR SHARE;
    SELECT count(*) INTO member_count FROM group_members WHERE group_id=task_group;
    IF member_count<>2 OR NOT EXISTS(SELECT 1 FROM group_members WHERE group_id=task_group AND user_id=e.actor_uid AND role='owner')
        OR NOT EXISTS(SELECT 1 FROM group_members WHERE group_id=task_group AND user_id=e.bot_uid) THEN
        RAISE EXCEPTION 'publication description membership changed';
    END IF;
    SELECT * INTO r FROM skillhub_publication_repair_rounds WHERE entry_id=NEW.entry_id AND round_number=NEW.round_number;
    IF NOT FOUND OR NEW.round_number<>(SELECT count(*) FROM skillhub_publication_repair_rounds WHERE entry_id=NEW.entry_id) THEN
        RAISE EXCEPTION 'publication description round is not current';
    END IF;
    IF TG_TABLE_NAME='skillhub_publication_description_drafts' THEN
        IF r.result IS NOT NULL OR r.lease_until<=clock_timestamp()
            OR NEW.lease_worker IS DISTINCT FROM r.lease_worker OR NEW.lease_token IS DISTINCT FROM r.lease_token
            OR NEW.lease_generation IS DISTINCT FROM r.lease_generation
            OR NEW.input->>'taskId' IS DISTINCT FROM t.task_id
            OR NEW.input->>'taskIdentityHash' IS DISTINCT FROM t.identity_hash::TEXT
            OR NEW.input->'round' IS DISTINCT FROM to_jsonb(NEW.round_number)
            OR NEW.input->>'roundIdentityHash' IS DISTINCT FROM r.identity_hash::TEXT
            OR NEW.input->'evidence' IS DISTINCT FROM r.input->'evidence'
            OR NEW.input->'proposal'->>'field' IS DISTINCT FROM 'description'
            OR jsonb_typeof(NEW.input->'proposal'->'text') IS DISTINCT FROM 'string'
            OR octet_length(NEW.input->'proposal'->>'text') NOT BETWEEN 1 AND 8192 THEN
            RAISE EXCEPTION 'publication description proposal binding or lease is invalid';
        END IF;
        IF ((NEW.input->'proposal'->>'relativePath'='skill.json' AND NOT (NEW.input->'proposal' ? 'entryIndex'))
            OR (NEW.input->'proposal'->>'relativePath'='SKILL.md'
                AND jsonb_typeof(NEW.input->'proposal'->'entryIndex')='number'
                AND NEW.input->'proposal'->>'entryIndex' ~ '^(0|[1-9][0-9]{0,15})$'
                AND (NEW.input->'proposal'->>'entryIndex')::NUMERIC<=9007199254740991)) IS NOT TRUE THEN
            RAISE EXCEPTION 'publication description target is invalid';
        END IF;
    ELSE
        SELECT * INTO d FROM skillhub_publication_description_drafts WHERE entry_id=NEW.entry_id AND round_number=NEW.round_number;
        IF NOT FOUND OR NEW.actor_uid IS DISTINCT FROM e.actor_uid OR NEW.confirmation_key IS DISTINCT FROM d.confirmation_key
            OR NEW.confirmed_at<d.created_at OR r.result->>'outcome' IS DISTINCT FROM 'waiting_confirmation'
            OR r.result->'evidence' IS DISTINCT FROM d.input->'evidence' THEN
            RAISE EXCEPTION 'publication description exact confirmation is invalid';
        END IF;
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_draft ON skillhub_publication_description_drafts;
CREATE TRIGGER guard_skillhub_publication_description_draft BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_drafts
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description();
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_confirmation ON skillhub_publication_description_confirmations;
CREATE TRIGGER guard_skillhub_publication_description_confirmation BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_confirmations
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description();
`
