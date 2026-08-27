-- A lease token is deliberately cleared on every terminal transition. Retain
-- the non-secret Worker attempt identifier separately so a completed or failed
-- preflight remains traceable after its lease has been released.
ALTER TABLE worker_jobs ADD COLUMN IF NOT EXISTS last_attempt_id TEXT;

UPDATE worker_jobs
   SET last_attempt_id = attempt_id
 WHERE last_attempt_id IS NULL
   AND attempt_id IS NOT NULL;

-- 000007 is already applied in deployed environments. Replace its controlled
-- projection in this new migration rather than changing its checksum.
GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;
SET LOCAL ROLE platform_worker_repository_owner;

-- Backfill jobs completed before this column existed from the validated Worker
-- stage event. This role owns worker_job_events and already has the narrow
-- worker_jobs DML grant from 000002; platform_migrator receives neither.
UPDATE worker_jobs AS job
   SET last_attempt_id = (
      SELECT event.payload_json ->> 'attempt_id' AS attempt_id
        FROM worker_job_events AS event
       WHERE event.job_id = job.job_id
         AND event.event_type = 'worker.stage'
         AND jsonb_typeof(event.payload_json -> 'attempt_id') = 'string'
         AND event.payload_json ->> 'attempt_id' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
       ORDER BY event.event_id DESC
       LIMIT 1
  )
 WHERE job.last_attempt_id IS NULL
   AND EXISTS (
       SELECT 1
         FROM worker_job_events AS event
        WHERE event.job_id = job.job_id
          AND event.event_type = 'worker.stage'
          AND jsonb_typeof(event.payload_json -> 'attempt_id') = 'string'
          AND event.payload_json ->> 'attempt_id' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
   );

CREATE OR REPLACE FUNCTION dataset_preflight_projection(p_dataset_id TEXT)
RETURNS TABLE(
    job_id TEXT,
    job_status TEXT,
    queue_position INTEGER,
    current_stage TEXT,
    attempt_id TEXT,
    lease_state TEXT,
    latest_event_id BIGINT,
    error_json JSONB
)
LANGUAGE sql
STABLE
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
    WITH selected_job AS (
        SELECT w.job_id, w.status, w.enqueue_seq, w.attempt_id,
               w.last_attempt_id, w.lease_expires_at, w.error_json
          FROM worker_jobs AS w
         WHERE w.dataset_id = p_dataset_id
           AND w.job_type = 'DATASET_PREFLIGHT'
         ORDER BY w.enqueue_seq DESC
         LIMIT 1
    ), latest_event AS (
        SELECT e.event_id
          FROM worker_job_events AS e
          JOIN selected_job AS j ON j.job_id = e.job_id
         ORDER BY e.event_id DESC
         LIMIT 1
    ), latest_stage AS (
        SELECT e.payload_json ->> 'stage' AS current_stage,
               CASE
                   WHEN jsonb_typeof(e.payload_json -> 'attempt_id') = 'string'
                    AND e.payload_json ->> 'attempt_id' ~ '^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$'
                   THEN e.payload_json ->> 'attempt_id'
               END AS attempt_id
          FROM worker_job_events AS e
          JOIN selected_job AS j ON j.job_id = e.job_id
         WHERE e.event_type = 'worker.stage'
         ORDER BY e.event_id DESC
         LIMIT 1
    )
    SELECT j.job_id,
           j.status,
           CASE WHEN j.status = 'QUEUED' THEN (
               SELECT count(*)::INTEGER
                 FROM worker_jobs AS queued_job
                WHERE queued_job.status = 'QUEUED'
                  AND queued_job.enqueue_seq <= j.enqueue_seq
           ) END,
           s.current_stage,
           COALESCE(j.attempt_id, j.last_attempt_id, s.attempt_id),
           CASE
               WHEN j.status = 'QUEUED' THEN 'NOT_CLAIMED'
               WHEN j.status IN ('RUNNING', 'CANCELLING') AND j.lease_expires_at > now() THEN 'ACTIVE'
               WHEN j.status IN ('RUNNING', 'CANCELLING') THEN 'EXPIRED'
               ELSE 'RELEASED'
           END,
           e.event_id,
           j.error_json
      FROM selected_job AS j
      LEFT JOIN latest_event AS e ON TRUE
      LEFT JOIN latest_stage AS s ON TRUE
$$;

REVOKE ALL ON FUNCTION dataset_preflight_projection(TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION dataset_preflight_projection(TEXT) TO web_api;

-- Persist this identity at claim time. Existing terminal transitions retain
-- last_attempt_id while still clearing attempt_id and every lease secret.
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
       SET status = 'RUNNING', attempt_id = p_attempt_id, last_attempt_id = p_attempt_id,
           lease_token = p_lease_token,
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

REVOKE ALL ON FUNCTION worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER) FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'algorithm_worker') THEN
        GRANT EXECUTE ON FUNCTION worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER) TO algorithm_worker;
    END IF;
END;
$$;

RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;
