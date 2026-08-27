-- Alarm result locators are API coordinates. They must never carry Worker
-- artifact names, storage paths, CSV rows, or file offsets.
--
-- 000002 deliberately transferred worker_commit_simulation to the dedicated
-- NOLOGIN owner. The migration runner is platform_migrator, so schema CREATE
-- is granted only while that owner replaces its existing function, then reset
-- before schema_migrations is written and revoked before commit.

UPDATE alarm_index
   SET result_locator_json = jsonb_build_object(
       'agent', agent,
       'original_running_index', original_running_index
   );

ALTER TABLE alarm_index
    ADD CONSTRAINT alarm_index_result_locator_exact_shape
    CHECK (
        jsonb_typeof(result_locator_json) = 'object'
        AND result_locator_json ?& ARRAY['agent', 'original_running_index']
        AND (result_locator_json - 'agent' - 'original_running_index') = '{}'::jsonb
        AND jsonb_typeof(result_locator_json -> 'agent') = 'number'
        AND jsonb_typeof(result_locator_json -> 'original_running_index') = 'number'
        AND result_locator_json ->> 'agent' = agent::TEXT
        AND result_locator_json ->> 'original_running_index' = original_running_index::TEXT
    );

GRANT CREATE ON SCHEMA public TO platform_worker_repository_owner;
SET LOCAL ROLE platform_worker_repository_owner;

CREATE OR REPLACE FUNCTION worker_commit_simulation(
    p_job_id TEXT,
    p_attempt_id TEXT,
    p_lease_token TEXT,
    p_manifest_sha256 CHAR(64),
    p_artifacts JSONB,
    p_alarms JSONB,
    p_stage_durations JSONB
) RETURNS BOOLEAN
LANGUAGE plpgsql
SECURITY DEFINER
SET search_path = pg_catalog, public
AS $$
DECLARE selected_job worker_jobs%ROWTYPE;
DECLARE required_name TEXT;
DECLARE artifact_manifest_sha CHAR(64);
BEGIN
    SELECT * INTO selected_job FROM worker_jobs
     WHERE job_id = p_job_id AND attempt_id = p_attempt_id AND lease_token = p_lease_token
       AND job_type = 'SIMULATION' AND status = 'RUNNING' AND lease_expires_at > now()
     FOR UPDATE;
    IF NOT FOUND THEN RETURN FALSE; END IF;

    IF p_alarms IS NOT NULL AND jsonb_typeof(p_alarms) <> 'array' THEN
        RAISE EXCEPTION 'alarms must be a JSON array' USING ERRCODE = '22023';
    END IF;
    IF EXISTS (
        SELECT 1
          FROM jsonb_array_elements(COALESCE(p_alarms, '[]'::jsonb)) AS submitted_alarm(value)
         WHERE jsonb_typeof(submitted_alarm.value) <> 'object'
            OR NOT (submitted_alarm.value ? 'agent')
            OR NOT (submitted_alarm.value ? 'original_running_index')
            OR NOT (submitted_alarm.value ? 'result_locator')
            OR jsonb_typeof(submitted_alarm.value -> 'agent') <> 'number'
            OR submitted_alarm.value ->> 'agent' NOT IN ('1', '2', '3')
            OR jsonb_typeof(submitted_alarm.value -> 'original_running_index') <> 'number'
            OR submitted_alarm.value ->> 'original_running_index' !~ '^(0|[1-9][0-9]*)$'
            OR length(submitted_alarm.value ->> 'original_running_index') > 19
            OR (length(submitted_alarm.value ->> 'original_running_index') = 19
                AND submitted_alarm.value ->> 'original_running_index' > '9223372036854775807')
            OR jsonb_typeof(submitted_alarm.value -> 'result_locator') <> 'object'
            OR NOT ((submitted_alarm.value -> 'result_locator') ?& ARRAY['agent', 'original_running_index'])
            OR ((submitted_alarm.value -> 'result_locator') - 'agent' - 'original_running_index') <> '{}'::jsonb
            OR jsonb_typeof(submitted_alarm.value -> 'result_locator' -> 'agent') <> 'number'
            OR jsonb_typeof(submitted_alarm.value -> 'result_locator' -> 'original_running_index') <> 'number'
            OR submitted_alarm.value -> 'result_locator' ->> 'agent' <> submitted_alarm.value ->> 'agent'
            OR submitted_alarm.value -> 'result_locator' ->> 'original_running_index' <> submitted_alarm.value ->> 'original_running_index'
            OR (submitted_alarm.value -> 'result_locator') <> jsonb_build_object(
                'agent', submitted_alarm.value -> 'agent',
                'original_running_index', submitted_alarm.value -> 'original_running_index'
            )
    ) THEN
        RAISE EXCEPTION 'invalid alarm result locator' USING ERRCODE = '22023';
    END IF;

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
    SELECT selected_job.run_id,item.agent,item.original_running_index,item.time_value,item.overall_alarm_level,item.alarm_type,item.reasons_json,item.load_status,
           jsonb_build_object('agent', item.agent, 'original_running_index', item.original_running_index)
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

REVOKE ALL ON FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) FROM PUBLIC;
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'algorithm_worker') THEN
        GRANT EXECUTE ON FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) TO algorithm_worker;
    END IF;
END;
$$;

RESET ROLE;
REVOKE CREATE ON SCHEMA public FROM platform_worker_repository_owner;
