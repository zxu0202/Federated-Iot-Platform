-- Local PostgreSQL integration fixture for an expired DATASET_PREFLIGHT lease.
-- Run as web_api after 000002 in a disposable test database. It rolls back all
-- rows and verifies that recovery returns the job count and does not leave the
-- dataset in VALIDATING.

BEGIN;

INSERT INTO datasets(
    dataset_id, display_name, original_filename, storage_key, sha256, size_bytes, columns_json,
    timezone, utc_offset, status, structural_statistics, warnings_json,
    preflight_contract_version, validation_started_at
) VALUES (
    'ds_recovery_preflight_fixture', 'Recovery fixture', 'recovery-fixture.csv',
    'datasets/ds_recovery_preflight_fixture/source.csv',
    repeat('a', 64), 1, '[]'::jsonb, 'Asia/Shanghai', '+08:00', 'VALIDATING',
    '{}'::jsonb, '[]'::jsonb, 'preprocessing.v1', now()
);

INSERT INTO worker_jobs(
    job_id, job_type, dataset_id, run_id, envelope_json, envelope_sha256,
    enqueue_seq, status, attempt_id, lease_token, lease_expires_at,
    last_heartbeat_at
) VALUES (
    'job_recovery_preflight_fixture', 'DATASET_PREFLIGHT',
    'ds_recovery_preflight_fixture', NULL, '{}'::jsonb, repeat('b', 64),
    nextval('enqueue_sequence'), 'RUNNING', 'attempt_recovery_fixture',
    'lease-token-recovery-fixture', now() - interval '1 second',
    now() - interval '61 seconds'
);

DO $$
DECLARE
    recovered BIGINT;
    dataset_status TEXT;
    dataset_error_code TEXT;
    dataset_error_message TEXT;
    job_status TEXT;
    job_error_code TEXT;
BEGIN
    SELECT worker_recover_expired_leases() INTO recovered;
    IF recovered <> 1 THEN
        RAISE EXCEPTION 'recovered job count = %, want 1', recovered;
    END IF;

    SELECT status, error_code, error_message
      INTO dataset_status, dataset_error_code, dataset_error_message
      FROM datasets
     WHERE dataset_id = 'ds_recovery_preflight_fixture';
    IF dataset_status <> 'INVALID'
       OR dataset_error_code <> 'PREFLIGHT_FAILED'
       OR dataset_error_message <> 'LEASE_LOST' THEN
        RAISE EXCEPTION 'preflight dataset recovery state is invalid: %, %, %',
            dataset_status, dataset_error_code, dataset_error_message;
    END IF;

    SELECT status, error_json ->> 'code'
      INTO job_status, job_error_code
      FROM worker_jobs
     WHERE job_id = 'job_recovery_preflight_fixture';
    IF job_status <> 'FAILED_RECOVERABLE' OR job_error_code <> 'LEASE_LOST' THEN
        RAISE EXCEPTION 'preflight job recovery state is invalid: %, %',
            job_status, job_error_code;
    END IF;
END;
$$;

ROLLBACK;
