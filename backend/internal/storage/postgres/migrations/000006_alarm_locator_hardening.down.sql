-- The safe locator rewrite is intentionally retained on downgrade. The former
-- function definition is restored only for migration reversibility; callers
-- should continue to supply the safe shape introduced by 000006.

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

ALTER TABLE alarm_index
    DROP CONSTRAINT alarm_index_result_locator_exact_shape;
