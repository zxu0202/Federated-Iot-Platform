-- A durable cancellation intent wins over a late Worker failure or an expired
-- lease. Non-cancelled jobs retain the existing LEASE_LOST recovery behavior.
GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;
SET LOCAL ROLE platform_worker_repository_owner;

CREATE OR REPLACE FUNCTION worker_recover_expired_leases()
RETURNS BIGINT LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE recovered_count BIGINT;
BEGIN
    PERFORM singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'scheduler control row is missing' USING ERRCODE = '55000';
    END IF;

    WITH expired_candidates AS (
        SELECT w.job_id, w.job_type, w.dataset_id, w.run_id, w.status AS prior_status,
               (w.status = 'CANCELLING' OR s.cancel_requested_at IS NOT NULL) AS cancellation_requested
          FROM worker_jobs AS w
          LEFT JOIN simulations AS s ON s.run_id = w.run_id
         WHERE w.status IN ('RUNNING', 'CANCELLING')
           AND w.lease_expires_at <= now()
         FOR UPDATE OF w
    ), expired AS (
        UPDATE worker_jobs AS w
           SET status = CASE WHEN candidate.cancellation_requested THEN 'CANCELLED' ELSE 'FAILED_RECOVERABLE' END,
               error_json = CASE
                   WHEN candidate.cancellation_requested THEN NULL
                   ELSE jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE)
               END,
               attempt_id = NULL,
               lease_token = NULL,
               lease_expires_at = NULL
          FROM expired_candidates AS candidate
         WHERE w.job_id = candidate.job_id
        RETURNING w.job_id, w.job_type, w.dataset_id, w.run_id, candidate.prior_status, candidate.cancellation_requested
    ), worker_events AS (
        INSERT INTO worker_job_events(job_id, run_id, event_type, payload_json)
        SELECT job_id, run_id, 'worker.state', jsonb_strip_nulls(jsonb_build_object(
            'status', CASE WHEN cancellation_requested THEN 'CANCELLED' ELSE 'FAILED_RECOVERABLE' END,
            'error', CASE WHEN cancellation_requested THEN NULL ELSE jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE) END
        ))
          FROM expired
    ), invalid_datasets AS (
        UPDATE datasets AS d
           SET status = 'INVALID', validation_finished_at = now(),
               error_code = 'PREFLIGHT_FAILED', error_message = 'LEASE_LOST'
          FROM expired AS e
         WHERE e.job_type = 'DATASET_PREFLIGHT'
           AND NOT e.cancellation_requested
           AND d.dataset_id = e.dataset_id
           AND d.status = 'VALIDATING'
    ), updated_simulations AS (
        UPDATE simulations AS s
           SET status = CASE WHEN e.cancellation_requested THEN 'CANCELLED' ELSE 'FAILED_RECOVERABLE' END,
               current_stage = NULL,
               finished_at = now(),
               artifact_state = 'INCOMPLETE',
               error_json = CASE
                   WHEN e.cancellation_requested THEN NULL
                   ELSE jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE)
               END
          FROM expired AS e
         WHERE e.run_id IS NOT NULL
           AND s.run_id = e.run_id
           AND s.status IN ('RUNNING', 'CANCELLING')
        RETURNING s.run_id, e.prior_status, e.cancellation_requested
    ), emitted_events AS (
        INSERT INTO simulation_events(run_id, event_type, payload_json)
        SELECT run_id, 'simulation.state', jsonb_strip_nulls(jsonb_build_object(
            'previous_status', prior_status,
            'status', CASE WHEN cancellation_requested THEN 'CANCELLED' ELSE 'FAILED_RECOVERABLE' END,
            'error', CASE WHEN cancellation_requested THEN NULL ELSE jsonb_build_object('code', 'LEASE_LOST', 'recoverable', TRUE) END
        ))
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

CREATE OR REPLACE FUNCTION worker_fail_job(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_error JSONB,
    p_recoverable BOOLEAN
) RETURNS BOOLEAN LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
DECLARE cancellation_requested BOOLEAN := FALSE;
DECLARE final_status TEXT := CASE WHEN p_recoverable THEN 'FAILED_RECOVERABLE' ELSE 'FAILED' END;
BEGIN
    -- Serialize terminal failure with the API cancellation transaction. Once
    -- cancellation holds this lock and commits its intent, failure must settle
    -- that attempt as CANCELLED instead of overwriting it with an error.
    PERFORM singleton FROM scheduler_control WHERE singleton = TRUE FOR UPDATE;
    IF NOT FOUND THEN
        RAISE EXCEPTION 'scheduler control row is missing' USING ERRCODE = '55000';
    END IF;

    SELECT * INTO selected_job
      FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND status IN ('RUNNING', 'CANCELLING') AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN
        RETURN FALSE;
    END IF;

    IF selected_job.run_id IS NOT NULL THEN
        SELECT cancel_requested_at IS NOT NULL INTO cancellation_requested
          FROM simulations
         WHERE run_id = selected_job.run_id;
        cancellation_requested := cancellation_requested OR selected_job.status = 'CANCELLING';
    END IF;

    IF cancellation_requested THEN
        UPDATE worker_jobs
           SET status = 'CANCELLED', error_json = NULL, attempt_id = NULL, lease_token = NULL, lease_expires_at = NULL
         WHERE job_id = p_job_id;
        UPDATE simulations
           SET status = 'CANCELLED', current_stage = NULL, finished_at = now(), artifact_state = 'INCOMPLETE', error_json = NULL
         WHERE run_id = selected_job.run_id AND status IN ('RUNNING', 'CANCELLING');
        INSERT INTO simulation_events(run_id, event_type, payload_json)
        VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object(
            'previous_status', selected_job.status, 'status', 'CANCELLED'
        ));
        PERFORM emit_queue_position_events();
        RETURN TRUE;
    END IF;

    UPDATE worker_jobs
       SET status = final_status, error_json = p_error, attempt_id = NULL, lease_token = NULL, lease_expires_at = NULL
     WHERE job_id = p_job_id;
    IF selected_job.run_id IS NULL THEN
        UPDATE datasets
           SET status = 'INVALID', validation_finished_at = now(), error_code = 'PREFLIGHT_FAILED',
               error_message = COALESCE(p_error ->> 'code', 'PREFLIGHT_FAILED')
         WHERE dataset_id = selected_job.dataset_id;
    ELSE
        UPDATE simulations
           SET status = final_status, current_stage = NULL, finished_at = now(), error_json = p_error, artifact_state = 'INCOMPLETE'
         WHERE run_id = selected_job.run_id;
        INSERT INTO simulation_events(run_id, event_type, payload_json)
        VALUES (selected_job.run_id, 'simulation.state', jsonb_build_object('status', final_status, 'error', p_error));
    END IF;
    RETURN TRUE;
END;
$$;

REVOKE ALL ON FUNCTION worker_recover_expired_leases() FROM PUBLIC;
REVOKE ALL ON FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION worker_recover_expired_leases() TO web_api;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'algorithm_worker') THEN
        GRANT EXECUTE ON FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) TO algorithm_worker;
    END IF;
END;
$$;

RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;
