-- Run after migrations 000001..000008 as a local database administrator in a
-- disposable PostgreSQL database. The fixture proves that the read projection
-- retains a terminal preflight attempt while keeping lease secrets private.

BEGIN;

INSERT INTO datasets(
    dataset_id, display_name, original_filename, storage_key, sha256, size_bytes,
    columns_json, timezone, utc_offset, status, structural_statistics, warnings_json,
    preflight_contract_version, validation_started_at, validation_finished_at
) VALUES
(
    'ds_attempt_terminal_fixture', 'Attempt terminal fixture', 'attempt-terminal.csv',
    'datasets/ds_attempt_terminal_fixture/source.csv', repeat('a', 64), 1,
    '[]'::jsonb, 'Asia/Shanghai', '+08:00', 'VALID', '{}'::jsonb, '[]'::jsonb,
    'preprocessing.v1', now(), now()
),
(
    'ds_attempt_queued_fixture', 'Attempt queued fixture', 'attempt-queued.csv',
    'datasets/ds_attempt_queued_fixture/source.csv', repeat('b', 64), 1,
    '[]'::jsonb, 'Asia/Shanghai', '+08:00', 'VALIDATING', '{}'::jsonb, '[]'::jsonb,
    'preprocessing.v1', now(), NULL
);

INSERT INTO worker_jobs(
    job_id, job_type, dataset_id, run_id, envelope_json, envelope_sha256,
    enqueue_seq, status, last_attempt_id
) VALUES
(
    'job_attempt_terminal_fixture', 'DATASET_PREFLIGHT', 'ds_attempt_terminal_fixture', NULL,
    '{}'::jsonb, repeat('c', 64), nextval('enqueue_sequence'), 'COMPLETED', 'attempt_terminal_fixture'
),
(
    'job_attempt_queued_fixture', 'DATASET_PREFLIGHT', 'ds_attempt_queued_fixture', NULL,
    '{}'::jsonb, repeat('d', 64), nextval('enqueue_sequence'), 'QUEUED', NULL
);

INSERT INTO worker_job_events(job_id, run_id, event_type, payload_json)
VALUES (
    'job_attempt_terminal_fixture', NULL, 'worker.stage',
    jsonb_build_object(
        'schema_version', 'worker.event.v1',
        'job_id', 'job_attempt_terminal_fixture',
        'attempt_id', 'attempt_terminal_fixture',
        'status', 'RUNNING',
        'stage', 'PREPROCESSING',
        'occurred_at', '2026-08-22T00:00:00Z',
        'diagnostics', '{}'::jsonb
    )
);

DO $$
DECLARE terminal_projection RECORD;
DECLARE queued_projection RECORD;
DECLARE terminal_json JSONB;
BEGIN
    SELECT * INTO terminal_projection
      FROM dataset_preflight_projection('ds_attempt_terminal_fixture');
    IF terminal_projection.job_status <> 'COMPLETED'
       OR terminal_projection.attempt_id <> 'attempt_terminal_fixture'
       OR terminal_projection.current_stage <> 'PREPROCESSING'
       OR terminal_projection.lease_state <> 'RELEASED'
       OR terminal_projection.latest_event_id IS NULL THEN
        RAISE EXCEPTION 'terminal preflight projection lost durable attempt identity';
    END IF;
    SELECT to_jsonb(projected) INTO terminal_json
      FROM dataset_preflight_projection('ds_attempt_terminal_fixture') AS projected;
    IF terminal_json ? 'lease_token' OR terminal_json ? 'lease_expires_at' THEN
        RAISE EXCEPTION 'preflight projection exposed a lease secret';
    END IF;

    SELECT * INTO queued_projection
      FROM dataset_preflight_projection('ds_attempt_queued_fixture');
    IF queued_projection.job_status <> 'QUEUED'
       OR queued_projection.attempt_id IS NOT NULL
       OR queued_projection.lease_state <> 'NOT_CLAIMED' THEN
        RAISE EXCEPTION 'unclaimed queued preflight has a non-null attempt identity';
    END IF;
END;
$$;

ROLLBACK;
