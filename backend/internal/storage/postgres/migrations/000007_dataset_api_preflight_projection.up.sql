-- The source filename is human-facing upload metadata, not a storage address.
-- Existing rows predate multipart filename retention, so preserve a safe display
-- alias where possible and use a neutral basename otherwise.
ALTER TABLE datasets ADD COLUMN original_filename TEXT;

UPDATE datasets
   SET original_filename = CASE
       WHEN char_length(display_name) BETWEEN 1 AND 255
        AND display_name !~ E'[\\\\/[:cntrl:]]'
        AND display_name NOT IN ('.', '..')
       THEN display_name
       ELSE 'source.csv'
   END;

ALTER TABLE datasets
    ALTER COLUMN original_filename SET NOT NULL,
    ADD CONSTRAINT datasets_original_filename_safe CHECK (
        char_length(original_filename) BETWEEN 1 AND 255
        AND original_filename !~ E'[\\\\/[:cntrl:]]'
        AND original_filename NOT IN ('.', '..')
    );

-- Web/API may read the worker-owned job/event facts only through this narrow,
-- fixed-search-path projection. It never receives worker table privileges.
GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;
SET LOCAL ROLE platform_worker_repository_owner;

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
               w.lease_expires_at, w.error_json
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
        SELECT e.payload_json ->> 'stage' AS current_stage
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
           j.attempt_id,
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

RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;
