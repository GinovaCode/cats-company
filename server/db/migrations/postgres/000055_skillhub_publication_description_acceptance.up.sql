CREATE TABLE IF NOT EXISTS skillhub_publication_description_acceptances (
    application_id VARCHAR(72) PRIMARY KEY REFERENCES skillhub_publication_description_applications(application_id) ON DELETE RESTRICT,
    result JSONB NOT NULL CHECK (jsonb_typeof(result)='object' AND octet_length(result::text)<=4096),
    report JSONB NOT NULL CHECK (jsonb_typeof(report)='object' AND octet_length(report::text)<=49152),
    identity_hash CHAR(64) NOT NULL CHECK (identity_hash ~ '^[a-f0-9]{64}$'),
    accepted_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp()
);
CREATE OR REPLACE FUNCTION guard_skillhub_publication_description_acceptance() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE
    a RECORD;
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
BEGIN
    IF TG_OP<>'INSERT' THEN
        RAISE EXCEPTION 'retained publication description acceptance is immutable';
    END IF;
    -- Match the application adapter lock order: entry before application.
    SELECT * INTO a FROM skillhub_publication_description_applications WHERE application_id=NEW.application_id;
    IF NOT FOUND OR a.result IS NULL THEN
        RAISE EXCEPTION 'publication description acceptance requires completed application';
    END IF;
    SELECT * INTO e FROM skillhub_publication_entries WHERE entry_id=a.entry_id FOR UPDATE;
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
    IF NOT FOUND OR EXISTS(SELECT 1 FROM skillhub_publication_cancel_intents WHERE entry_id=a.entry_id)
        OR EXISTS(SELECT 1 FROM skillhub_publication_commit_intents WHERE entry_id=a.entry_id) THEN
        RAISE EXCEPTION 'publication description application authority changed or entry settled';
    END IF;
    SELECT * INTO t FROM skillhub_publication_repair_tasks WHERE entry_id=a.entry_id;
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
    SELECT * INTO r FROM skillhub_publication_repair_rounds WHERE entry_id=a.entry_id AND round_number=a.round_number;
    IF NOT FOUND OR a.round_number<>(SELECT count(*) FROM skillhub_publication_repair_rounds WHERE entry_id=a.entry_id)
        OR r.result->>'outcome' IS DISTINCT FROM 'waiting_confirmation' THEN
        RAISE EXCEPTION 'publication description application round is not waiting for confirmation';
    END IF;
    SELECT * INTO d FROM skillhub_publication_description_drafts WHERE entry_id=a.entry_id AND round_number=a.round_number;
    IF NOT FOUND OR a.input->'draft' IS DISTINCT FROM d.input OR a.confirmation_key IS DISTINCT FROM d.confirmation_key
        OR a.input->>'confirmationKey' IS DISTINCT FROM d.confirmation_key::TEXT
        OR a.input->'receipt' IS DISTINCT FROM t.input->'receipt'
        OR r.result->'evidence' IS DISTINCT FROM d.input->'evidence'
        OR a.application_id IS DISTINCT FROM 'pubdesc_'||a.identity_hash THEN
        RAISE EXCEPTION 'publication description application exact binding is invalid';
    END IF;
    SELECT * INTO c FROM skillhub_publication_description_confirmations WHERE entry_id=a.entry_id AND round_number=a.round_number;
    IF NOT FOUND OR c.actor_uid IS DISTINCT FROM e.actor_uid OR c.confirmation_key IS DISTINCT FROM a.confirmation_key
        OR a.created_at<c.confirmed_at THEN
        RAISE EXCEPTION 'publication description application is not confirmed';
    END IF;
    IF NEW.result IS DISTINCT FROM a.result OR NEW.accepted_at<a.applied_at
        OR NEW.report->>'schemaVersion' IS DISTINCT FROM '1'
        OR (NEW.report->>'ruleVersion' ~ '^skillhub-publish-v[0-9]{1,4}\.[0-9]{1,4}\.[0-9]{1,4}$') IS NOT TRUE
        OR NEW.report->>'profile' IS DISTINCT FROM 'skillhub-publish-v1'
        OR (NEW.report->>'reportId' ~ '^[a-f0-9]{8}-[a-f0-9]{4}-4[a-f0-9]{3}-[89ab][a-f0-9]{3}-[a-f0-9]{12}$') IS NOT TRUE
        OR NEW.report->>'outcome' IS DISTINCT FROM 'passed'
        OR NEW.report->>'coverage' IS DISTINCT FROM 'complete'
        OR NEW.report->'truncated' IS DISTINCT FROM 'false'::JSONB
        OR NEW.report->>'nextAction' IS DISTINCT FROM 'none'
        OR NEW.report->>'candidateHash' IS DISTINCT FROM a.result->'derivation'->>'derivedCandidateHash'
        OR NEW.report->>'limitsFingerprint' IS DISTINCT FROM a.result->'derivation'->>'limitsFingerprint'
        OR jsonb_typeof(NEW.report->'issues') IS DISTINCT FROM 'array' THEN
        RAISE EXCEPTION 'publication description acceptance report binding is invalid';
    END IF;
    IF jsonb_array_length(NEW.report->'issues')>50
        OR EXISTS(SELECT 1 FROM jsonb_array_elements(NEW.report->'issues') i WHERE i->'blocking' IS DISTINCT FROM 'false'::JSONB) THEN
        RAISE EXCEPTION 'publication description acceptance report is blocking';
    END IF;
    RETURN NEW;
END;
$$;
DROP TRIGGER IF EXISTS guard_skillhub_publication_description_acceptance ON skillhub_publication_description_acceptances;
CREATE TRIGGER guard_skillhub_publication_description_acceptance BEFORE INSERT OR UPDATE OR DELETE ON skillhub_publication_description_acceptances
FOR EACH ROW EXECUTE FUNCTION guard_skillhub_publication_description_acceptance();
