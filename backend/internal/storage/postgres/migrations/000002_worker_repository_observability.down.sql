DO $$
BEGIN
    IF current_user <> 'platform_migrator'
       AND NOT (SELECT rolsuper FROM pg_roles WHERE rolname = current_user) THEN
        RAISE EXCEPTION 'Worker Repository migration requires platform_migrator' USING ERRCODE = '42501';
    END IF;
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'algorithm_worker') THEN
        REVOKE ALL ON FUNCTION worker_register_instance(TEXT, TEXT, TEXT),
            worker_heartbeat_instance(TEXT, TEXT),
            worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER),
            worker_heartbeat_for_worker(TEXT, TEXT, TEXT, TEXT, INTEGER),
            worker_cancellation_context(TEXT, TEXT, TEXT),
            worker_report_event(TEXT, TEXT, TEXT, JSONB)
        FROM algorithm_worker;
    END IF;
END;
$$;
DROP FUNCTION IF EXISTS worker_report_event(TEXT, TEXT, TEXT, JSONB);
DROP FUNCTION IF EXISTS worker_cancellation_context(TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS worker_heartbeat_for_worker(TEXT, TEXT, TEXT, TEXT, INTEGER);
DROP FUNCTION IF EXISTS worker_claim_next_job_for_worker(TEXT, TEXT, TEXT, INTEGER);
DROP FUNCTION IF EXISTS worker_heartbeat_instance(TEXT, TEXT);
DROP FUNCTION IF EXISTS worker_register_instance(TEXT, TEXT, TEXT);
DROP FUNCTION IF EXISTS worker_recover_expired_leases();
DROP TABLE IF EXISTS worker_job_events;
DROP TABLE IF EXISTS worker_instances;
ALTER FUNCTION emit_queue_position_events() OWNER TO web_api;
ALTER FUNCTION emit_queue_position_events() RESET search_path;
ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) SECURITY INVOKER;
ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) OWNER TO web_api;
ALTER FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER) RESET search_path;
ALTER FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) SECURITY INVOKER;
ALTER FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) OWNER TO web_api;
ALTER FUNCTION worker_heartbeat(TEXT, TEXT, TEXT, INTEGER) RESET search_path;
ALTER FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) SECURITY INVOKER;
ALTER FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) OWNER TO web_api;
ALTER FUNCTION worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB) RESET search_path;
ALTER FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) SECURITY INVOKER;
ALTER FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) OWNER TO web_api;
ALTER FUNCTION worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)) RESET search_path;
ALTER FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) SECURITY INVOKER;
ALTER FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) OWNER TO web_api;
ALTER FUNCTION worker_confirm_cancel(TEXT, TEXT, TEXT) RESET search_path;
ALTER FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) SECURITY INVOKER;
ALTER FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) OWNER TO web_api;
ALTER FUNCTION worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN) RESET search_path;
ALTER FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) SECURITY INVOKER;
ALTER FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) OWNER TO web_api;
ALTER FUNCTION worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB) RESET search_path;
GRANT EXECUTE ON FUNCTION worker_claim_next_job(TEXT, TEXT, INTEGER),
    worker_heartbeat(TEXT, TEXT, TEXT, INTEGER),
    worker_report_stage(TEXT, TEXT, TEXT, TEXT, SMALLINT, JSONB),
    worker_complete_preflight(TEXT, TEXT, TEXT, CHAR(64), JSONB, CHAR(64)),
    worker_confirm_cancel(TEXT, TEXT, TEXT),
    worker_fail_job(TEXT, TEXT, TEXT, JSONB, BOOLEAN),
    worker_commit_simulation(TEXT, TEXT, TEXT, CHAR(64), JSONB, JSONB, JSONB)
TO PUBLIC;
GRANT EXECUTE ON FUNCTION emit_queue_position_events() TO PUBLIC;
