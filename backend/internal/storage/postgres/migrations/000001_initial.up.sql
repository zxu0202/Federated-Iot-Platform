-- Federated IoT Platform PostgreSQL control-plane schema.
-- This migration is PostgreSQL-only by approved S1 scope.

CREATE TABLE IF NOT EXISTS schema_migrations (
    version TEXT PRIMARY KEY,
    checksum_sha256 TEXT NOT NULL,
    applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE scheduler_control (
    singleton BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (singleton),
    preflight_queue_capacity INTEGER NOT NULL DEFAULT 4 CHECK (preflight_queue_capacity > 0),
    simulation_wait_capacity INTEGER NOT NULL DEFAULT 10 CHECK (simulation_wait_capacity = 10),
    event_retention_per_run INTEGER NOT NULL DEFAULT 2000 CHECK (event_retention_per_run > 0),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO scheduler_control (singleton) VALUES (TRUE) ON CONFLICT (singleton) DO NOTHING;

CREATE TABLE datasets (
    dataset_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    storage_key TEXT NOT NULL UNIQUE,
    sha256 CHAR(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    size_bytes BIGINT NOT NULL CHECK (size_bytes > 0),
    columns_json JSONB NOT NULL,
    timezone TEXT NOT NULL,
    utc_offset TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('UPLOADING', 'VALIDATING', 'VALID', 'INVALID')),
    structural_statistics JSONB NOT NULL,
    warnings_json JSONB NOT NULL DEFAULT '[]'::jsonb,
    preflight_contract_version TEXT,
    preflight_summary_json JSONB,
    preflight_summary_sha256 CHAR(64),
    validation_started_at TIMESTAMPTZ,
    validation_finished_at TIMESTAMPTZ,
    error_code TEXT,
    error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status = 'VALID') = (preflight_summary_sha256 IS NOT NULL)),
    CHECK (preflight_summary_sha256 IS NULL OR preflight_summary_sha256 ~ '^[0-9a-f]{64}$')
);

CREATE TABLE parameter_profiles (
    version_id TEXT PRIMARY KEY,
    mode TEXT NOT NULL CHECK (mode IN ('REFERENCE', 'CUSTOM')),
    display_name TEXT NOT NULL,
    base_version_id TEXT REFERENCES parameter_profiles(version_id),
    contract_version TEXT NOT NULL,
    shared_parameters JSONB NOT NULL,
    agents_json JSONB NOT NULL,
    fixed_items JSONB NOT NULL,
    normalized_json JSONB NOT NULL,
    normalized_sha256 CHAR(64) NOT NULL UNIQUE CHECK (normalized_sha256 ~ '^[0-9a-f]{64}$'),
    immutable BOOLEAN NOT NULL DEFAULT TRUE CHECK (immutable),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((mode = 'REFERENCE' AND base_version_id IS NULL) OR mode = 'CUSTOM')
);

CREATE UNIQUE INDEX parameter_profiles_one_reference ON parameter_profiles ((mode)) WHERE mode = 'REFERENCE';

CREATE TABLE load_mapping_profiles (
    version_id TEXT PRIMARY KEY,
    display_name TEXT NOT NULL,
    mapping_type TEXT NOT NULL CHECK (mapping_type = 'identity'),
    parameters_json JSONB NOT NULL,
    result_unit TEXT NOT NULL,
    normalized_json JSONB NOT NULL,
    normalized_sha256 CHAR(64) NOT NULL UNIQUE CHECK (normalized_sha256 ~ '^[0-9a-f]{64}$'),
    immutable BOOLEAN NOT NULL DEFAULT TRUE CHECK (immutable),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE simulations (
    run_id TEXT PRIMARY KEY,
    display_name TEXT,
    dataset_id TEXT NOT NULL REFERENCES datasets(dataset_id),
    parameter_profile_version_id TEXT NOT NULL REFERENCES parameter_profiles(version_id),
    load_mapping_version_id TEXT NOT NULL REFERENCES load_mapping_profiles(version_id),
    run_mode TEXT NOT NULL CHECK (run_mode IN ('REFERENCE', 'CUSTOM')),
    status TEXT NOT NULL CHECK (status IN ('CREATED', 'VALIDATING', 'QUEUED', 'RUNNING', 'CANCELLING', 'GENERATING_ARTIFACTS', 'COMPLETED', 'CANCELLED', 'FAILED', 'FAILED_RECOVERABLE')),
    current_stage TEXT CHECK (current_stage IS NULL OR current_stage IN ('PREPROCESSING', 'LOCAL_TRAINING', 'ANCHOR_AGGREGATING', 'GLOBAL_DISTILLING', 'CALIBRATING', 'TESTING')),
    enqueue_seq BIGINT NOT NULL UNIQUE,
    cancel_requested_at TIMESTAMPTZ,
    cancel_reason TEXT,
    started_at TIMESTAMPTZ,
    finished_at TIMESTAMPTZ,
    error_json JSONB,
    artifact_state TEXT NOT NULL DEFAULT 'NOT_STARTED' CHECK (artifact_state IN ('NOT_STARTED', 'GENERATING', 'COMMITTED', 'INCOMPLETE')),
    last_heartbeat_at TIMESTAMPTZ,
    attempt_id TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((status IN ('RUNNING', 'CANCELLING')) = (current_stage IS NOT NULL)),
    CHECK (status <> 'COMPLETED' OR artifact_state = 'COMMITTED')
);

CREATE UNIQUE INDEX simulations_single_execution_slot
    ON simulations ((TRUE))
    WHERE status IN ('RUNNING', 'CANCELLING', 'GENERATING_ARTIFACTS');
CREATE INDEX simulations_history_idx ON simulations (created_at DESC, run_id DESC);
CREATE INDEX simulations_queue_idx ON simulations (enqueue_seq) WHERE status = 'QUEUED';

CREATE TABLE simulation_snapshots (
    run_id TEXT PRIMARY KEY REFERENCES simulations(run_id) ON DELETE RESTRICT,
    snapshot_json JSONB NOT NULL,
    snapshot_sha256 CHAR(64) NOT NULL UNIQUE CHECK (snapshot_sha256 ~ '^[0-9a-f]{64}$'),
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE worker_jobs (
    job_id TEXT PRIMARY KEY,
    job_type TEXT NOT NULL CHECK (job_type IN ('DATASET_PREFLIGHT', 'SIMULATION')),
    dataset_id TEXT NOT NULL REFERENCES datasets(dataset_id),
    run_id TEXT UNIQUE REFERENCES simulations(run_id),
    envelope_json JSONB NOT NULL,
    envelope_sha256 CHAR(64) NOT NULL CHECK (envelope_sha256 ~ '^[0-9a-f]{64}$'),
    enqueue_seq BIGINT NOT NULL UNIQUE,
    status TEXT NOT NULL CHECK (status IN ('QUEUED', 'RUNNING', 'CANCELLING', 'COMPLETED', 'CANCELLED', 'FAILED', 'FAILED_RECOVERABLE')),
    attempt_id TEXT,
    lease_token TEXT,
    lease_expires_at TIMESTAMPTZ,
    last_heartbeat_at TIMESTAMPTZ,
    error_json JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    CHECK ((job_type = 'SIMULATION') = (run_id IS NOT NULL)),
    CHECK ((status IN ('RUNNING', 'CANCELLING')) = (attempt_id IS NOT NULL AND lease_token IS NOT NULL AND lease_expires_at IS NOT NULL))
);

CREATE UNIQUE INDEX worker_jobs_single_execution_slot
    ON worker_jobs ((TRUE))
    WHERE status IN ('RUNNING', 'CANCELLING');
CREATE INDEX worker_jobs_queue_idx ON worker_jobs (enqueue_seq) WHERE status = 'QUEUED';

CREATE TABLE simulation_events (
    event_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES simulations(run_id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload_json JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX simulation_events_run_idx ON simulation_events (run_id, event_id);

CREATE TABLE artifacts (
    run_id TEXT NOT NULL REFERENCES simulations(run_id) ON DELETE RESTRICT,
    name TEXT NOT NULL CHECK (position('/' in name) = 0 AND position(chr(92) in name) = 0 AND position('..' in name) = 0),
    relative_path TEXT NOT NULL CHECK (relative_path !~ '^/' AND relative_path !~ '(^|/)[.][.](/|$)'),
    media_type TEXT NOT NULL,
    size_bytes BIGINT NOT NULL CHECK (size_bytes >= 0),
    sha256 CHAR(64) NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    required BOOLEAN NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (run_id, name),
    UNIQUE (run_id, relative_path)
);

CREATE TABLE alarm_index (
    alarm_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    run_id TEXT NOT NULL REFERENCES simulations(run_id) ON DELETE CASCADE,
    agent SMALLINT NOT NULL CHECK (agent IN (1, 2, 3)),
    original_running_index BIGINT NOT NULL CHECK (original_running_index >= 0),
    time_value TIMESTAMPTZ,
    overall_alarm_level TEXT NOT NULL CHECK (overall_alarm_level IN ('None', 'Notice', 'Warning', 'Alarm')),
    alarm_type TEXT NOT NULL,
    reasons_json JSONB NOT NULL,
    load_status TEXT NOT NULL CHECK (load_status IN ('Light load', 'Normal load', 'Heavy load', 'Unknown')),
    result_locator_json JSONB NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX alarm_index_lookup_idx ON alarm_index (run_id, agent, original_running_index);

CREATE TABLE idempotency_keys (
    idempotency_key TEXT PRIMARY KEY CHECK (length(idempotency_key) BETWEEN 16 AND 128),
    request_sha256 CHAR(64) NOT NULL CHECK (request_sha256 ~ '^[0-9a-f]{64}$'),
    run_id TEXT NOT NULL REFERENCES simulations(run_id) ON DELETE RESTRICT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE SEQUENCE enqueue_sequence AS BIGINT START WITH 1;

CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION reject_immutable_update() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.immutable THEN
        RAISE EXCEPTION 'immutable resource cannot be updated' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION protect_terminal_simulation() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
    IF OLD.status IN ('COMPLETED', 'CANCELLED', 'FAILED', 'FAILED_RECOVERABLE') AND NEW IS DISTINCT FROM OLD THEN
        RAISE EXCEPTION 'terminal simulation cannot be changed' USING ERRCODE = '55000';
    END IF;
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION retain_simulation_events() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE retention INTEGER;
BEGIN
    SELECT event_retention_per_run INTO retention FROM scheduler_control WHERE singleton = TRUE;
    DELETE FROM simulation_events
     WHERE run_id = NEW.run_id
       AND event_id IN (
           SELECT event_id FROM simulation_events
            WHERE run_id = NEW.run_id
            ORDER BY event_id DESC
            OFFSET retention
       );
    RETURN NEW;
END;
$$;

CREATE OR REPLACE FUNCTION emit_queue_position_events() RETURNS VOID LANGUAGE plpgsql AS $$
BEGIN
    INSERT INTO simulation_events(run_id,event_type,payload_json)
    SELECT run_id, 'queue.position', jsonb_build_object('status','QUEUED','queue_position',queue_position,'queued_count',queued_count)
      FROM (
          SELECT run_id, row_number() OVER (ORDER BY enqueue_seq)::INTEGER AS queue_position,
                 count(*) OVER ()::INTEGER AS queued_count
            FROM simulations
           WHERE status = 'QUEUED'
      ) queued;
END;
$$;

CREATE TRIGGER datasets_updated_at BEFORE UPDATE ON datasets FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER parameter_profiles_updated_at BEFORE UPDATE ON parameter_profiles FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER load_mapping_profiles_updated_at BEFORE UPDATE ON load_mapping_profiles FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER simulations_updated_at BEFORE UPDATE ON simulations FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER worker_jobs_updated_at BEFORE UPDATE ON worker_jobs FOR EACH ROW EXECUTE FUNCTION set_updated_at();
CREATE TRIGGER parameter_profiles_immutable BEFORE UPDATE ON parameter_profiles FOR EACH ROW EXECUTE FUNCTION reject_immutable_update();
CREATE TRIGGER load_mapping_profiles_immutable BEFORE UPDATE ON load_mapping_profiles FOR EACH ROW EXECUTE FUNCTION reject_immutable_update();
CREATE TRIGGER simulations_terminal BEFORE UPDATE ON simulations FOR EACH ROW EXECUTE FUNCTION protect_terminal_simulation();
CREATE TRIGGER simulation_events_retention AFTER INSERT ON simulation_events FOR EACH ROW EXECUTE FUNCTION retain_simulation_events();

-- The functions below are the PostgreSQL Worker Repository boundary. Worker
-- credentials receive EXECUTE only in deployment provisioning; the Web/API owns
-- direct application-table writes and migrations.
CREATE OR REPLACE FUNCTION worker_claim_next_job(
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_lease_seconds INTEGER
) RETURNS TABLE(job_id TEXT, job_type TEXT, run_id TEXT, envelope_json JSONB, lease_expires_at TIMESTAMPTZ)
LANGUAGE plpgsql AS $$
DECLARE
    selected_job worker_jobs%ROWTYPE;
BEGIN
    IF p_lease_seconds < 1 OR p_lease_seconds > 600 THEN
        RAISE EXCEPTION 'invalid lease duration' USING ERRCODE = '22023';
    END IF;

    SELECT * INTO selected_job
      FROM worker_jobs
     WHERE status = 'QUEUED'
     ORDER BY enqueue_seq
     FOR UPDATE SKIP LOCKED
     LIMIT 1;

    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE worker_jobs
       SET status = 'RUNNING', attempt_id = p_attempt_id, lease_token = p_lease_token,
           lease_expires_at = now() + make_interval(secs => p_lease_seconds),
           last_heartbeat_at = now()
     WHERE worker_jobs.job_id = selected_job.job_id;

    IF selected_job.job_type = 'SIMULATION' THEN
        UPDATE simulations
           SET status = 'RUNNING', current_stage = 'PREPROCESSING', started_at = COALESCE(started_at, now()),
               last_heartbeat_at = now(), attempt_id = p_attempt_id
         WHERE simulations.run_id = selected_job.run_id AND simulations.status = 'QUEUED';
        IF NOT FOUND THEN
            RAISE EXCEPTION 'simulation was not claimable' USING ERRCODE = '55000';
        END IF;
        INSERT INTO simulation_events (run_id, event_type, payload_json)
        VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('previous_status', 'QUEUED', 'status', 'RUNNING'));
        PERFORM emit_queue_position_events();
    END IF;

    RETURN QUERY
    SELECT selected_job.job_id, selected_job.job_type, selected_job.run_id, selected_job.envelope_json,
           now() + make_interval(secs => p_lease_seconds);
END;
$$;

CREATE OR REPLACE FUNCTION worker_heartbeat(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_lease_seconds INTEGER
) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
BEGIN
    UPDATE worker_jobs
       SET lease_expires_at = now() + make_interval(secs => p_lease_seconds), last_heartbeat_at = now()
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND status IN ('RUNNING', 'CANCELLING') AND lease_expires_at > now();
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    UPDATE simulations s
       SET last_heartbeat_at = now()
      FROM worker_jobs w
     WHERE w.job_id = p_job_id AND w.run_id = s.run_id;
    RETURN TRUE;
END;
$$;

CREATE OR REPLACE FUNCTION worker_report_stage(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_stage TEXT,
    p_agent SMALLINT,
    p_diagnostics JSONB
) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
BEGIN
    SELECT * INTO selected_job FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND status IN ('RUNNING', 'CANCELLING') AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;
    IF selected_job.run_id IS NULL THEN RETURN TRUE; END IF;
    IF p_stage NOT IN ('PREPROCESSING', 'LOCAL_TRAINING', 'ANCHOR_AGGREGATING', 'GLOBAL_DISTILLING', 'CALIBRATING', 'TESTING') THEN
        RAISE EXCEPTION 'invalid stage' USING ERRCODE = '22023';
    END IF;
    UPDATE simulations SET current_stage = p_stage WHERE run_id = selected_job.run_id;
    INSERT INTO simulation_events (run_id, event_type, payload_json)
    VALUES (selected_job.run_id, 'simulation.stage', jsonb_strip_nulls(jsonb_build_object('status', (SELECT status FROM simulations WHERE run_id = selected_job.run_id), 'current_stage', p_stage, 'agent', p_agent, 'diagnostics', p_diagnostics)));
    RETURN TRUE;
END;
$$;

CREATE OR REPLACE FUNCTION worker_complete_preflight(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_input_sha256 CHAR(64),
    p_summary JSONB,
    p_summary_sha256 CHAR(64)
) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
BEGIN
    SELECT * INTO selected_job FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND job_type = 'DATASET_PREFLIGHT' AND status = 'RUNNING' AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;
    UPDATE datasets
       SET status = 'VALID', preflight_summary_json = p_summary, preflight_summary_sha256 = p_summary_sha256,
           validation_finished_at = now(), error_code = NULL, error_message = NULL
     WHERE dataset_id = selected_job.dataset_id AND status = 'VALIDATING' AND sha256 = p_input_sha256;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'preflight dataset hash or status mismatch' USING ERRCODE = '55000';
    END IF;
    UPDATE worker_jobs SET status = 'COMPLETED', attempt_id = NULL, lease_token = NULL, lease_expires_at = NULL WHERE job_id = p_job_id;
    RETURN TRUE;
END;
$$;

CREATE OR REPLACE FUNCTION worker_confirm_cancel(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT
) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
BEGIN
    SELECT * INTO selected_job FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND status = 'CANCELLING' AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;
    UPDATE worker_jobs SET status = 'CANCELLED', attempt_id = NULL, lease_token = NULL, lease_expires_at = NULL WHERE job_id = p_job_id;
    IF selected_job.run_id IS NOT NULL THEN
        UPDATE simulations SET status = 'CANCELLED', current_stage = NULL, finished_at = now(), artifact_state = 'INCOMPLETE'
         WHERE run_id = selected_job.run_id AND status = 'CANCELLING';
        INSERT INTO simulation_events (run_id, event_type, payload_json)
        VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('previous_status', 'CANCELLING', 'status', 'CANCELLED'));
        PERFORM emit_queue_position_events();
    END IF;
    RETURN TRUE;
END;
$$;

CREATE OR REPLACE FUNCTION worker_fail_job(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_error JSONB,
    p_recoverable BOOLEAN
) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
DECLARE final_status TEXT := CASE WHEN p_recoverable THEN 'FAILED_RECOVERABLE' ELSE 'FAILED' END;
BEGIN
    SELECT * INTO selected_job FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND status IN ('RUNNING', 'CANCELLING') AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;
    UPDATE worker_jobs SET status = final_status, error_json = p_error, attempt_id = NULL, lease_token = NULL, lease_expires_at = NULL WHERE job_id = p_job_id;
    IF selected_job.run_id IS NULL THEN
        UPDATE datasets SET status = 'INVALID', validation_finished_at = now(), error_code = 'PREFLIGHT_FAILED', error_message = COALESCE(p_error ->> 'code', 'PREFLIGHT_FAILED')
         WHERE dataset_id = selected_job.dataset_id;
    ELSE
        UPDATE simulations SET status = final_status, current_stage = NULL, finished_at = now(), error_json = p_error, artifact_state = 'INCOMPLETE'
         WHERE run_id = selected_job.run_id;
        INSERT INTO simulation_events (run_id, event_type, payload_json)
        VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('status', final_status, 'error', p_error));
    END IF;
    RETURN TRUE;
END;
$$;

CREATE OR REPLACE FUNCTION worker_commit_simulation(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_manifest_sha256 CHAR(64),
    p_artifacts JSONB,
    p_alarms JSONB,
    p_stage_durations JSONB
) RETURNS BOOLEAN LANGUAGE plpgsql AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
DECLARE required_name TEXT;
DECLARE artifact_manifest_sha CHAR(64);
BEGIN
    SELECT * INTO selected_job FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND job_type = 'SIMULATION' AND status = 'RUNNING' AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;

    FOREACH required_name IN ARRAY ARRAY[
        'run_manifest.json', 'preprocessing_summary.json', 'agent_partition_summary.csv', 'feature_schema.json',
        'anchor_summary.json', 'metrics.csv', 'results_agent_1.csv', 'results_agent_2.csv', 'results_agent_3.csv',
        'alarms.csv', 'diagnostics.json', 'artifact_manifest.json'
    ] LOOP
        IF NOT EXISTS (SELECT 1 FROM jsonb_to_recordset(p_artifacts) AS item(name TEXT, required BOOLEAN) WHERE item.name = required_name AND item.required) THEN
            RAISE EXCEPTION 'required artifact % is missing', required_name USING ERRCODE = '22023';
        END IF;
    END LOOP;
    SELECT item.sha256 INTO artifact_manifest_sha
      FROM jsonb_to_recordset(p_artifacts) AS item(name TEXT, sha256 CHAR(64))
     WHERE item.name = 'artifact_manifest.json';
    IF artifact_manifest_sha IS DISTINCT FROM p_manifest_sha256 THEN
        RAISE EXCEPTION 'artifact manifest SHA-256 mismatch' USING ERRCODE = '22023';
    END IF;

    UPDATE simulations SET status='GENERATING_ARTIFACTS', current_stage=NULL, artifact_state='GENERATING'
     WHERE run_id = selected_job.run_id AND status='RUNNING';
    IF NOT FOUND THEN RAISE EXCEPTION 'simulation is not in a committable state' USING ERRCODE = '55000'; END IF;
    INSERT INTO simulation_events(run_id,event_type,payload_json)
    VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('previous_status','RUNNING','status','GENERATING_ARTIFACTS'));

    INSERT INTO artifacts(run_id,name,relative_path,media_type,size_bytes,sha256,required)
    SELECT selected_job.run_id,item.name,item.relative_path,item.media_type,item.size_bytes,item.sha256,item.required
      FROM jsonb_to_recordset(p_artifacts) AS item(
        name TEXT, relative_path TEXT, media_type TEXT, size_bytes BIGINT, sha256 CHAR(64), required BOOLEAN
      );
    INSERT INTO alarm_index(run_id,agent,original_running_index,time_value,overall_alarm_level,alarm_type,reasons_json,load_status,result_locator_json)
    SELECT selected_job.run_id,item.agent,item.original_running_index,item.time_value,item.overall_alarm_level,item.alarm_type,item.reasons_json,item.load_status,item.result_locator_json
      FROM jsonb_to_recordset(COALESCE(p_alarms, '[]'::jsonb)) AS item(
        agent SMALLINT, original_running_index BIGINT, time_value TIMESTAMPTZ, overall_alarm_level TEXT,
        alarm_type TEXT, reasons_json JSONB, load_status TEXT, result_locator_json JSONB
      );
    UPDATE simulations SET status='COMPLETED', artifact_state='COMMITTED', finished_at=now()
     WHERE run_id = selected_job.run_id AND status='GENERATING_ARTIFACTS';
    UPDATE worker_jobs SET status='COMPLETED', attempt_id=NULL, lease_token=NULL, lease_expires_at=NULL WHERE job_id=p_job_id;
    INSERT INTO simulation_events(run_id,event_type,payload_json)
    VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('previous_status','GENERATING_ARTIFACTS','status','COMPLETED'));
    INSERT INTO simulation_events(run_id,event_type,payload_json)
    VALUES (selected_job.run_id, 'artifact.committed', jsonb_build_object('status','COMPLETED','artifact_count',jsonb_array_length(p_artifacts),'manifest_sha256',p_manifest_sha256,'stage_durations_ms',p_stage_durations));
    RETURN TRUE;
END;
$$;
