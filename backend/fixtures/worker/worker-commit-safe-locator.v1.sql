-- Local PostgreSQL integration fixture for 000006. Run after migrations as a
-- local database administrator in a disposable database. It rolls back all
-- rows and proves that invalid Worker input is atomic, safe input is accepted,
-- and a retry does not duplicate artifacts, alarms, or events.

BEGIN;

INSERT INTO parameter_profiles(
    version_id, mode, display_name, base_version_id, contract_version,
    shared_parameters, agents_json, fixed_items, normalized_json, normalized_sha256
) VALUES (
    'reference-v1', 'REFERENCE', 'Reference-compatible', NULL, 'parameter-profile.v1',
    '{}'::jsonb, '[]'::jsonb, '{}'::jsonb, '{}'::jsonb, repeat('1', 64)
) ON CONFLICT (version_id) DO NOTHING;

INSERT INTO parameter_profiles(
    version_id, mode, display_name, base_version_id, contract_version,
    shared_parameters, agents_json, fixed_items, normalized_json, normalized_sha256
) VALUES (
    'profile_locator_fixture', 'CUSTOM', 'Locator fixture', 'reference-v1', 'parameter-profile.v1',
    '{}'::jsonb, '[]'::jsonb, '{}'::jsonb, '{}'::jsonb, repeat('2', 64)
);

INSERT INTO load_mapping_profiles(
    version_id, display_name, mapping_type, parameters_json, result_unit, normalized_json, normalized_sha256
) VALUES (
    'identity-v1', 'Identity', 'identity', '{}'::jsonb, 'A', '{}'::jsonb, repeat('3', 64)
) ON CONFLICT (version_id) DO NOTHING;

INSERT INTO datasets(
    dataset_id, display_name, original_filename, storage_key, sha256, size_bytes, columns_json,
    timezone, utc_offset, status, structural_statistics, warnings_json,
    preflight_contract_version, preflight_summary_json, preflight_summary_sha256,
    validation_started_at, validation_finished_at
) VALUES (
    'ds_locator_fixture', 'Locator fixture', 'locator-fixture.csv', 'datasets/ds_locator_fixture/source.csv',
    repeat('4', 64), 1, '[]'::jsonb, 'Asia/Shanghai', '+08:00', 'VALID',
    '{}'::jsonb, '[]'::jsonb, 'preprocessing.v1', '{}'::jsonb, repeat('5', 64), now(), now()
);

INSERT INTO simulations(
    run_id, display_name, dataset_id, parameter_profile_version_id,
    load_mapping_version_id, run_mode, status, current_stage, enqueue_seq,
    artifact_state, attempt_id
) VALUES (
    'run_locator_fixture', 'Locator fixture', 'ds_locator_fixture', 'profile_locator_fixture',
    'identity-v1', 'CUSTOM', 'RUNNING', 'PREPROCESSING', nextval('enqueue_sequence'),
    'NOT_STARTED', 'attempt_locator_fixture'
);

INSERT INTO simulation_snapshots(run_id, snapshot_json, snapshot_sha256)
VALUES ('run_locator_fixture', '{}'::jsonb, repeat('6', 64));

INSERT INTO worker_jobs(
    job_id, job_type, dataset_id, run_id, envelope_json, envelope_sha256,
    enqueue_seq, status, attempt_id, lease_token, lease_expires_at, last_heartbeat_at
) VALUES (
    'job_locator_fixture', 'SIMULATION', 'ds_locator_fixture', 'run_locator_fixture',
    '{}'::jsonb, repeat('7', 64), nextval('enqueue_sequence'), 'RUNNING',
    'attempt_locator_fixture', 'lease-token-locator-fixture', now() + interval '5 minutes', now()
);

DO $$
DECLARE
    artifacts JSONB;
    valid_alarms JSONB := jsonb_build_array(jsonb_build_object(
        'agent', 1,
        'original_running_index', 14059,
        'time_value', '2026-08-17T08:30:00+08:00',
        'overall_alarm_level', 'Warning',
        'alarm_type', 'LOAD',
        'reasons_json', jsonb_build_array('load threshold'),
        'load_status', 'Heavy load',
        'result_locator', jsonb_build_object('agent', 1, 'original_running_index', 14059)
    ));
    invalid_alarms JSONB := jsonb_build_array(jsonb_build_object(
        'agent', 1,
        'original_running_index', 14059,
        'time_value', '2026-08-17T08:30:00+08:00',
        'overall_alarm_level', 'Warning',
        'alarm_type', 'LOAD',
        'reasons_json', jsonb_build_array('load threshold'),
        'load_status', 'Heavy load',
        'result_locator', jsonb_build_object('agent', 1, 'original_running_index', 14059, 'artifact', 'alarms.csv')
    ));
    accepted BOOLEAN;
    retried BOOLEAN;
    current_status TEXT;
    artifact_count BIGINT;
    alarm_count BIGINT;
    event_count BIGINT;
    locator JSONB;
BEGIN
    SELECT jsonb_agg(jsonb_build_object(
        'name', required_name,
        'relative_path', format('runs/run_locator_fixture/committed/%s', required_name),
        'media_type', 'application/octet-stream',
        'size_bytes', 1,
        'sha256', CASE WHEN required_name = 'artifact_manifest.json' THEN repeat('b', 64) ELSE repeat('a', 64) END,
        'required', TRUE
    )) INTO artifacts
    FROM unnest(ARRAY[
        'run_manifest.json', 'preprocessing_summary.json', 'agent_partition_summary.csv', 'feature_schema.json',
        'anchor_summary.json', 'metrics.csv', 'results_agent_1.csv', 'results_agent_2.csv', 'results_agent_3.csv',
        'alarms.csv', 'diagnostics.json', 'artifact_manifest.json'
    ]) AS required_artifact(required_name);

    BEGIN
        PERFORM worker_commit_simulation(
            'job_locator_fixture', 'attempt_locator_fixture', 'lease-token-locator-fixture',
            repeat('b', 64), artifacts, invalid_alarms, '{}'::jsonb
        );
        RAISE EXCEPTION 'invalid alarm locator was accepted';
    EXCEPTION WHEN SQLSTATE '22023' THEN
        NULL;
    END;

    SELECT status INTO current_status FROM worker_jobs WHERE job_id = 'job_locator_fixture';
    SELECT count(*) INTO artifact_count FROM artifacts WHERE run_id = 'run_locator_fixture';
    SELECT count(*) INTO alarm_count FROM alarm_index WHERE run_id = 'run_locator_fixture';
    SELECT count(*) INTO event_count FROM simulation_events WHERE run_id = 'run_locator_fixture';
    IF current_status <> 'RUNNING' OR artifact_count <> 0 OR alarm_count <> 0 OR event_count <> 0 THEN
        RAISE EXCEPTION 'invalid locator changed terminal commit state: %, %, %, %', current_status, artifact_count, alarm_count, event_count;
    END IF;

    SELECT worker_commit_simulation(
        'job_locator_fixture', 'attempt_locator_fixture', 'lease-token-locator-fixture',
        repeat('b', 64), artifacts, valid_alarms, '{}'::jsonb
    ) INTO accepted;
    SELECT worker_commit_simulation(
        'job_locator_fixture', 'attempt_locator_fixture', 'lease-token-locator-fixture',
        repeat('b', 64), artifacts, valid_alarms, '{}'::jsonb
    ) INTO retried;

    SELECT count(*) INTO artifact_count FROM artifacts WHERE run_id = 'run_locator_fixture';
    SELECT count(*) INTO alarm_count FROM alarm_index WHERE run_id = 'run_locator_fixture';
    SELECT count(*) INTO event_count FROM simulation_events WHERE run_id = 'run_locator_fixture';
    SELECT result_locator_json INTO locator FROM alarm_index WHERE run_id = 'run_locator_fixture';
    IF accepted IS DISTINCT FROM TRUE OR retried IS DISTINCT FROM FALSE
       OR artifact_count <> 12 OR alarm_count <> 1 OR event_count <> 3
       OR locator <> jsonb_build_object('agent', 1, 'original_running_index', 14059) THEN
        RAISE EXCEPTION 'safe locator commit/retry is not idempotent: %, %, %, %, %, %', accepted, retried, artifact_count, alarm_count, event_count, locator;
    END IF;
END;
$$;

ROLLBACK;
