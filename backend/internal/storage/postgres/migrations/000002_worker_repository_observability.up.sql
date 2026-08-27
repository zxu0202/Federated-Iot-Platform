CREATE TABLE worker_instances (
    worker_id TEXT PRIMARY KEY CHECK (worker_id ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    worker_contract_version TEXT NOT NULL CHECK (worker_contract_version = 'worker.task.v1'),
    worker_version TEXT NOT NULL CHECK (worker_version ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'),
    registered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    last_heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX worker_instances_observation_idx ON worker_instances (worker_contract_version, last_heartbeat_at DESC);

CREATE TABLE worker_job_events (
    event_id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES worker_jobs(job_id) ON DELETE CASCADE,
    run_id TEXT REFERENCES simulations(run_id) ON DELETE CASCADE,
    event_type TEXT NOT NULL,
    payload_json JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX worker_job_events_job_idx ON worker_job_events (job_id, event_id);

-- Worker registration and liveness are durable control-plane facts. The
-- Worker receives only function EXECUTE permissions; it has no table access.
-- Legacy Worker Repository functions are also converted here because 000001
-- may already be applied before this migration is introduced.
--
-- This migration must run only from the dedicated migration service. The
-- runtime web_api role must not own, replace, or inherit this owner role.
DO $$
BEGIN
    IF current_user <> 'platform_migrator'
       AND NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
        RAISE EXCEPTION 'Worker Repository migration requires platform_migrator' USING ERRCODE = '42501';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
         WHERE rolname = 'platform_worker_repository_owner' AND NOT rolcanlogin
    ) THEN
        RAISE EXCEPTION 'platform_worker_repository_owner must be a NOLOGIN role' USING ERRCODE = '55000';
    END IF;
    BEGIN
        EXECUTE 'SET LOCAL ROLE platform_worker_repository_owner';
        EXECUTE 'SET LOCAL ROLE platform_migrator';
    EXCEPTION WHEN insufficient_privilege THEN
        RAISE EXCEPTION 'platform_migrator must be permitted to SET ROLE platform_worker_repository_owner' USING ERRCODE = '42501';
    END;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'web_api')
       AND pg_has_role('web_api', 'platform_worker_repository_owner', 'MEMBER') THEN
        RAISE EXCEPTION 'web_api must not inherit the Worker Repository owner role' USING ERRCODE = '55000';
    END IF;
END;
$$;

ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) SECURITY DEFINER;
ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) SET search_path = pg_catalog, public;
ALTER FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) SECURITY DEFINER;
ALTER FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) SET search_path = pg_catalog, public;
ALTER FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) SECURITY DEFINER;
ALTER FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) SET search_path = pg_catalog, public;
ALTER FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) SECURITY DEFINER;
ALTER FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) SET search_path = pg_catalog, public;
ALTER FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) SECURITY DEFINER;
ALTER FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) SET search_path = pg_catalog, public;
ALTER FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) SECURITY DEFINER;
ALTER FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) SET search_path = pg_catalog, public;
ALTER FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) SECURITY DEFINER;
ALTER FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) SET search_path = pg_catalog, public;

-- The recovery operation and the Worker claim path share scheduler_control's
-- single row lock. An expired lease is made terminal before another queued
-- job can be claimed, preventing a restarted Worker from bypassing the sole
-- execution slot.
CREATE OR REPLACE FUNCTION worker_recover_expired_leases()
RETURNS BIGINT LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE recovered_count BIGINT;
BEGIN
    PERFORM singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'scheduler control row is missing' USING ERRCODE = '55000';
    END IF;

    WITH expired AS (
        UPDATE worker_jobs
           SET status = 'FAILED_RECOVERABLE',
               error_json = jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE),
               attempt_id = NULL,
               lease_token = NULL,
               lease_expires_at = NULL
         WHERE status IN ('RUNNING', 'CANCELLING')
           AND lease_expires_at <= now()
        RETURNING job_id, job_type, dataset_id, run_id
    ), worker_events AS (
        INSERT INTO worker_job_events(job_id, run_id, event_type, payload_json)
        SELECT job_id, run_id, 'worker.state', jsonb_build_object(
            'status', 'FAILED_RECOVERABLE',
            'error', jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE)
        )
          FROM expired
    ), invalid_datasets AS (
        UPDATE datasets d
           SET status = 'INVALID', validation_finished_at = now(),
               error_code = 'PREFLIGHT_FAILED', error_message = 'LEASE_LOST'
          FROM expired e
         WHERE e.job_type = 'DATASET_PREFLIGHT'
           AND d.dataset_id = e.dataset_id
           AND d.status = 'VALIDATING'
    ), updated_simulations AS (
        UPDATE simulations s
           SET status = 'FAILED_RECOVERABLE', current_stage = NULL, finished_at = now(),
               artifact_state = 'INCOMPLETE',
               error_json = jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE)
          FROM expired e
         WHERE e.run_id IS NOT NULL
           AND s.run_id = e.run_id
           AND s.status IN ('RUNNING', 'CANCELLING')
        RETURNING s.run_id
    ), emitted_events AS (
        INSERT INTO simulation_events(run_id, event_type, payload_json)
        SELECT run_id, 'simulation.state', jsonb_build_object(
            'status', 'FAILED_RECOVERABLE',
            'error', jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE)
        )
          FROM updated_simulations
        RETURNING run_id
    )
    SELECT count(*) INTO recovered_count FROM expired;

    IF recovered_count > 0 THEN
        PERFORM emit_queue_position_events();
    END IF;
    RETURN recovered_count;
END;
$$;

CREATE OR REPLACE FUNCTION worker_register_instance(
    p_worker_id TEXT,
    p_contract_version TEXT,
    p_worker_version TEXT
) RETURNS TIMESTAMPTZ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE observed_at TIMESTAMPTZ;
BEGIN
    IF p_worker_id !~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' THEN
        RAISE EXCEPTION 'invalid worker identifier' USING ERRCODE = '22023';
    END IF;
    IF p_contract_version <> 'worker.task.v1' THEN
        RAISE EXCEPTION 'unsupported worker contract' USING ERRCODE = '22023';
    END IF;
    IF p_worker_version !~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' THEN
        RAISE EXCEPTION 'invalid worker version' USING ERRCODE = '22023';
    END IF;

    INSERT INTO worker_instances(worker_id, worker_contract_version, worker_version)
    VALUES (p_worker_id, p_contract_version, p_worker_version)
    ON CONFLICT (worker_id) DO UPDATE
       SET worker_contract_version = EXCLUDED.worker_contract_version,
           worker_version = EXCLUDED.worker_version,
           last_heartbeat_at = now()
    RETURNING last_heartbeat_at INTO observed_at;
    RETURN observed_at;
END;
$$;

CREATE OR REPLACE FUNCTION worker_heartbeat_instance(
    p_worker_id TEXT,
    p_contract_version TEXT
) RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
    UPDATE worker_instances
       SET last_heartbeat_at = now()
     WHERE worker_id = p_worker_id
       AND worker_contract_version = p_contract_version;
    RETURN FOUND;
END;
$$;

CREATE OR REPLACE FUNCTION worker_claim_next_job_for_worker(
    p_worker_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_lease_seconds INTEGER
) RETURNS TABLE(job_id TEXT, job_type TEXT, run_id TEXT, envelope_json JSONB, lease_expires_at TIMESTAMPTZ)
LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
DECLARE claimed_envelope JSONB;
BEGIN
    IF p_lease_seconds < 1 OR p_lease_seconds > 600 THEN
        RAISE EXCEPTION 'invalid lease duration' USING ERRCODE = '22023';
    END IF;
    IF p_attempt_id !~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$' OR length(p_lease_token) < 16 THEN
        RAISE EXCEPTION 'invalid lease identity' USING ERRCODE = '22023';
    END IF;

    UPDATE worker_instances
       SET last_heartbeat_at = now()
     WHERE worker_id = p_worker_id
       AND worker_contract_version = 'worker.task.v1'
       AND last_heartbeat_at > now() - make_interval(secs => p_lease_seconds);
    IF NOT FOUND THEN
        RAISE EXCEPTION 'worker is not registered or heartbeat is stale' USING ERRCODE = '55000';
    END IF;

    PERFORM singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'scheduler control row is missing' USING ERRCODE = '55000';
    END IF;
    PERFORM worker_recover_expired_leases();
    IF EXISTS (
        SELECT 1 FROM worker_jobs AS active_job
         WHERE active_job.status IN ('RUNNING', 'CANCELLING')
           AND active_job.lease_expires_at > now()
    ) THEN
        RETURN;
    END IF;

    SELECT * INTO selected_job
      FROM worker_jobs AS queued_job
     WHERE queued_job.status = 'QUEUED'
     ORDER BY queued_job.enqueue_seq
     FOR UPDATE SKIP LOCKED
     LIMIT 1;
    IF NOT FOUND THEN
        RETURN;
    END IF;

    UPDATE worker_jobs AS claimed_job
       SET status = 'RUNNING', attempt_id = p_attempt_id, lease_token = p_lease_token,
           lease_expires_at = now() + make_interval(secs => p_lease_seconds),
           last_heartbeat_at = now()
     WHERE claimed_job.job_id = selected_job.job_id;

    claimed_envelope := selected_job.envelope_json || jsonb_build_object(
        'attempt_id', p_attempt_id,
        'lease_token', p_lease_token
    );
    IF selected_job.job_type = 'SIMULATION' THEN
        claimed_envelope := jsonb_set(
            claimed_envelope,
            ARRAY['output', 'relative_tmp_directory'],
            to_jsonb(format('runs/%s/tmp/%s', selected_job.run_id, p_attempt_id)),
            FALSE
        );
        UPDATE simulations
           SET status = 'RUNNING', current_stage = 'PREPROCESSING', started_at = COALESCE(started_at, now()),
               last_heartbeat_at = now(), attempt_id = p_attempt_id
         WHERE simulations.run_id = selected_job.run_id AND simulations.status = 'QUEUED';
        IF NOT FOUND THEN
            RAISE EXCEPTION 'simulation was not claimable' USING ERRCODE = '55000';
        END IF;
        INSERT INTO simulation_events(run_id, event_type, payload_json)
        VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('previous_status', 'QUEUED', 'status', 'RUNNING'));
        PERFORM emit_queue_position_events();
    ELSE
        claimed_envelope := jsonb_set(
            claimed_envelope,
            ARRAY['output', 'relative_tmp_directory'],
            to_jsonb(format('datasets/%s/preflight/tmp/%s', selected_job.dataset_id, p_attempt_id)),
            FALSE
        );
    END IF;

    RETURN QUERY
    SELECT selected_job.job_id, selected_job.job_type, selected_job.run_id, claimed_envelope,
           now() + make_interval(secs => p_lease_seconds);
END;
$$;

CREATE OR REPLACE FUNCTION worker_heartbeat_for_worker(
    p_worker_id TEXT,
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_lease_seconds INTEGER
) RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
BEGIN
    IF p_lease_seconds < 1 OR p_lease_seconds > 600 THEN
        RAISE EXCEPTION 'invalid lease duration' USING ERRCODE = '22023';
    END IF;
    UPDATE worker_instances
       SET last_heartbeat_at = now()
     WHERE worker_id = p_worker_id
       AND worker_contract_version = 'worker.task.v1';
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
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
    INSERT INTO worker_job_events(job_id, run_id, event_type, payload_json)
    SELECT w.job_id, w.run_id, 'worker.heartbeat', jsonb_build_object('status', w.status)
      FROM worker_jobs w
     WHERE w.job_id = p_job_id;
    INSERT INTO simulation_events(run_id, event_type, payload_json)
    SELECT w.run_id, 'heartbeat', jsonb_build_object('status', w.status)
      FROM worker_jobs w
     WHERE w.job_id = p_job_id AND w.run_id IS NOT NULL;
    RETURN TRUE;
END;
$$;

CREATE OR REPLACE FUNCTION worker_cancellation_context(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT
) RETURNS TABLE(cancel_requested BOOLEAN, cancel_requested_at TIMESTAMPTZ, lease_valid BOOLEAN)
LANGUAGE sql STABLE SECURITY DEFINER SET search_path = pg_catalog, public AS $$
    SELECT (w.status = 'CANCELLING' OR s.cancel_requested_at IS NOT NULL),
           s.cancel_requested_at,
           TRUE
      FROM worker_jobs w
      LEFT JOIN simulations s ON s.run_id = w.run_id
     WHERE w.job_id = p_job_id
       AND w.attempt_id = p_attempt_id
       AND w.lease_token = p_lease_token
       AND w.status IN ('RUNNING', 'CANCELLING')
       AND w.lease_expires_at > now()
$$;

CREATE OR REPLACE FUNCTION worker_report_event(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_event JSONB
) RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
DECLARE event_agent SMALLINT;
BEGIN
    SELECT * INTO selected_job
      FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND status IN ('RUNNING', 'CANCELLING') AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;
    IF p_event ->> 'schema_version' <> 'worker.event.v1'
       OR p_event ->> 'job_id' <> p_job_id
       OR p_event ->> 'attempt_id' <> p_attempt_id
       OR p_event ->> 'status' <> selected_job.status
       OR p_event ->> 'stage' NOT IN ('PREPROCESSING', 'LOCAL_TRAINING', 'ANCHOR_AGGREGATING', 'GLOBAL_DISTILLING', 'CALIBRATING', 'TESTING')
       OR jsonb_typeof(p_event -> 'occurred_at') <> 'string'
       OR jsonb_typeof(p_event -> 'diagnostics') <> 'object'
       OR pg_column_size(p_event) > 65536 THEN
        RAISE EXCEPTION 'invalid worker event' USING ERRCODE = '22023';
    END IF;
    IF selected_job.run_id IS NULL THEN
        IF p_event ->> 'run_id' IS NOT NULL THEN
            RAISE EXCEPTION 'preflight event has a run identifier' USING ERRCODE = '22023';
        END IF;
    ELSIF p_event ->> 'run_id' IS DISTINCT FROM selected_job.run_id THEN
        RAISE EXCEPTION 'worker event run identifier mismatch' USING ERRCODE = '22023';
    END IF;
    IF p_event ? 'agent' AND p_event -> 'agent' <> 'null'::JSONB THEN
        IF jsonb_typeof(p_event -> 'agent') <> 'number'
           OR p_event ->> 'agent' !~ '^[1-3]$' THEN
            RAISE EXCEPTION 'invalid worker event agent' USING ERRCODE = '22023';
        END IF;
        event_agent := (p_event ->> 'agent')::SMALLINT;
    END IF;

    INSERT INTO worker_job_events(job_id, run_id, event_type, payload_json)
    VALUES (selected_job.job_id, selected_job.run_id, 'worker.stage', p_event);
    IF selected_job.run_id IS NOT NULL THEN
        UPDATE simulations SET current_stage = p_event ->> 'stage'
         WHERE run_id = selected_job.run_id AND status = selected_job.status;
        INSERT INTO simulation_events(run_id, event_type, payload_json)
        VALUES (
            selected_job.run_id,
            'simulation.stage',
            jsonb_strip_nulls(jsonb_build_object(
                'status', selected_job.status,
                'current_stage', p_event ->> 'stage',
                'agent', event_agent,
                'diagnostics', p_event -> 'diagnostics'
            ))
        );
    END IF;
    RETURN TRUE;
END;
$$;

GRANT USAGE ON SCHEMA public TO platform_worker_repository_owner;
GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE datasets, simulations, worker_jobs, simulation_events, artifacts, alarm_index, scheduler_control TO platform_worker_repository_owner;
GRANT USAGE, SELECT ON SEQUENCE simulation_events_event_id_seq, alarm_index_alarm_id_seq TO platform_worker_repository_owner;

-- 000001 is created by platform_migrator on a clean deployment. The Web/API
-- uses direct repository SQL, so it receives only its observed DML surface;
-- schema_migrations remains read-only to support the serve-time checksum gate.
GRANT USAGE ON SCHEMA public TO web_api;
REVOKE CREATE ON SCHEMA public FROM web_api;
GRANT SELECT ON TABLE schema_migrations, scheduler_control, datasets, parameter_profiles,
    load_mapping_profiles, simulations, simulation_snapshots, worker_jobs,
    simulation_events, idempotency_keys, worker_instances TO web_api;
GRANT INSERT ON TABLE datasets, parameter_profiles, load_mapping_profiles,
    simulations, simulation_snapshots, worker_jobs, simulation_events,
    idempotency_keys TO web_api;
-- PostgreSQL requires UPDATE for the Web/API's SELECT ... FOR UPDATE
-- admission locks on scheduler_control and idempotency_keys. The simulation
-- state transition locks simulations and its joined snapshot row; worker_jobs
-- is updated directly.
GRANT UPDATE ON TABLE scheduler_control, simulations, simulation_snapshots, worker_jobs, idempotency_keys TO web_api;
GRANT DELETE ON TABLE simulation_events TO web_api;
GRANT USAGE ON SEQUENCE enqueue_sequence, simulation_events_event_id_seq TO web_api;
GRANT EXECUTE ON FUNCTION emit_queue_position_events(), worker_recover_expired_leases() TO web_api;

REVOKE ALL ON TABLE worker_instances, worker_job_events FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_recover_expired_leases() FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_register_instance(TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_heartbeat_instance(TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_heartbeat_for_worker(TEXT, TEXT, TEXT, TEXT, INTEGER) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_cancellation_context(TEXT, TEXT, TEXT) FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_report_event(TEXT, TEXT, TEXT, JSONB) FROM PUBLIC;
REVOKE ALL ON FUNCTION emit_queue_position_events() FROM PUBLIC;

-- These 000001 trigger functions retain platform_migrator ownership and
-- invoker behavior, but must not remain directly executable by PUBLIC.
REVOKE ALL ON FUNCTION set_updated_at() FROM PUBLIC;
REVOKE ALL ON FUNCTION reject_immutable_update() FROM PUBLIC;
REVOKE ALL ON FUNCTION protect_terminal_simulation() FROM PUBLIC;
REVOKE ALL ON FUNCTION retain_simulation_events() FROM PUBLIC;

DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'algorithm_worker') THEN
        GRANT USAGE ON SCHEMA public TO algorithm_worker;
        GRANT EXECUTE ON FUNCTION worker_register_instance(TEXT, TEXT, TEXT),
            worker_heartbeat_instance(TEXT, TEXT),
            worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER),
            worker_heartbeat_for_worker(TEXT, TEXT, TEXT, TEXT, INTEGER),
            worker_cancellation_context(TEXT, TEXT, TEXT),
            worker_report_event(TEXT, TEXT, TEXT, JSONB),
            worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)),
            worker_confirm_cancel(TEXT, TEXT, TEXT),
            worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN),
            worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB)
        TO algorithm_worker;
    END IF;
END;
$$;

-- All ACL changes above execute while platform_migrator still owns fresh
-- deployment objects. Ownership changes are deliberately last: NOINHERIT
-- membership is not relied on as an implicit owner privilege after transfer.
-- PostgreSQL requires the target owner to hold CREATE on the containing
-- schema for each ownership transfer. This transaction-scoped migration
-- grants that capability only for the transfer and removes it before commit.
GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;
ALTER TABLE worker_instances OWNER TO platform_worker_repository_owner;
ALTER TABLE worker_job_events OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_recover_expired_leases() OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_register_instance(TEXT, TEXT, TEXT) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_heartbeat_instance(TEXT, TEXT) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_heartbeat_for_worker(TEXT, TEXT, TEXT, TEXT, INTEGER) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_cancellation_context(TEXT, TEXT, TEXT) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION worker_report_event(TEXT, TEXT, TEXT, JSONB) OWNER TO platform_worker_repository_owner;
ALTER FUNCTION emit_queue_position_events() SET search_path = pg_catalog, public;
ALTER FUNCTION emit_queue_position_events() OWNER TO platform_worker_repository_owner;
REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;
