-- PostgreSQL ACL gate for the M1 dedicated migration identity.
-- Run this only against an isolated clean-deploy project or an approved upgrade.

DO $$
DECLARE
    expected_function_count INTEGER := 10;
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
        WHERE rolname = 'platform_worker_repository_owner'
          AND NOT rolcanlogin AND NOT rolsuper AND NOT rolcreaterole AND NOT rolcreatedb AND NOT rolbypassrls
    ) THEN
        RAISE EXCEPTION 'platform_worker_repository_owner is missing or not a controlled NOLOGIN role';
    END IF;
    IF NOT EXISTS (
        SELECT 1 FROM pg_roles
        WHERE rolname = 'platform_migrator'
          AND rolcanlogin AND NOT rolinherit AND NOT rolsuper AND NOT rolcreaterole AND NOT rolcreatedb AND NOT rolbypassrls
    ) THEN
        RAISE EXCEPTION 'platform_migrator is missing or does not have the restricted migration role attributes';
    END IF;
    IF NOT pg_has_role('platform_migrator', 'platform_worker_repository_owner', 'MEMBER') THEN
        RAISE EXCEPTION 'platform_migrator is not a member of the controlled owner role';
    END IF;
    IF pg_has_role('web_api', 'platform_worker_repository_owner', 'MEMBER')
       OR pg_has_role('algorithm_worker', 'platform_worker_repository_owner', 'MEMBER') THEN
        RAISE EXCEPTION 'a runtime role inherits the controlled owner role';
    END IF;
    IF has_schema_privilege('platform_worker_repository_owner', 'public', 'CREATE') THEN
        RAISE EXCEPTION 'platform_worker_repository_owner retains CREATE on public';
    END IF;
    IF has_schema_privilege('web_api', 'public', 'CREATE')
       OR has_schema_privilege('algorithm_worker', 'public', 'CREATE') THEN
        RAISE EXCEPTION 'a runtime role can create objects in public';
    END IF;

    IF EXISTS (
        SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND has_function_privilege('public', p.oid, 'EXECUTE')
    ) THEN
        RAISE EXCEPTION 'PUBLIC retains EXECUTE on a public schema function';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND pg_get_userbyid(p.proowner) = 'web_api'
    ) THEN
        RAISE EXCEPTION 'web_api owns a public schema function';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND p.proname LIKE 'worker_%'
          AND (NOT p.prosecdef
               OR pg_get_userbyid(p.proowner) <> 'platform_worker_repository_owner'
               OR NOT (p.proconfig @> ARRAY['search_path=pg_catalog, public']))
    ) THEN
        RAISE EXCEPTION 'a Worker Repository function lacks controlled ownership, SECURITY DEFINER, or fixed search_path';
    END IF;
    IF NOT EXISTS (
        SELECT 1
        FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public'
          AND p.oid = to_regprocedure('worker_recover_expired_leases()')
          AND pg_get_userbyid(p.proowner) = 'platform_worker_repository_owner'
          AND p.prosecdef
          AND p.proconfig @> ARRAY['search_path=pg_catalog, public']
          AND NOT has_function_privilege('public', p.oid, 'EXECUTE')
          AND has_function_privilege('web_api', p.oid, 'EXECUTE')
          AND NOT has_function_privilege('algorithm_worker', p.oid, 'EXECUTE')
    ) THEN
        RAISE EXCEPTION 'worker_recover_expired_leases ACL or SECURITY DEFINER contract is invalid';
    END IF;

    IF (SELECT count(*) FROM (
        VALUES
            (to_regprocedure('worker_register_instance(text,text,text)')),
            (to_regprocedure('worker_heartbeat_instance(text,text)')),
            (to_regprocedure('worker_claim_next_job_for_worker(text,text,text,integer)')),
            (to_regprocedure('worker_heartbeat_for_worker(text,text,text,text,integer)')),
            (to_regprocedure('worker_cancellation_context(text,text,text)')),
            (to_regprocedure('worker_report_event(text,text,text,jsonb)')),
            (to_regprocedure('worker_complete_preflight(text,text,text,character,jsonb,character)')),
            (to_regprocedure('worker_confirm_cancel(text,text,text)')),
            (to_regprocedure('worker_fail_job(text,text,text,jsonb,boolean)')),
            (to_regprocedure('worker_commit_simulation(text,text,text,character,jsonb,jsonb,jsonb)'))
    ) AS expected(oid) WHERE oid IS NOT NULL) <> expected_function_count THEN
        RAISE EXCEPTION 'the expected precise Worker function set is incomplete';
    END IF;
    IF EXISTS (
        SELECT 1 FROM (
            VALUES
                (to_regprocedure('worker_register_instance(text,text,text)')),
                (to_regprocedure('worker_heartbeat_instance(text,text)')),
                (to_regprocedure('worker_claim_next_job_for_worker(text,text,text,integer)')),
                (to_regprocedure('worker_heartbeat_for_worker(text,text,text,text,integer)')),
                (to_regprocedure('worker_cancellation_context(text,text,text)')),
                (to_regprocedure('worker_report_event(text,text,text,jsonb)')),
                (to_regprocedure('worker_complete_preflight(text,text,text,character,jsonb,character)')),
                (to_regprocedure('worker_confirm_cancel(text,text,text)')),
                (to_regprocedure('worker_fail_job(text,text,text,jsonb,boolean)')),
                (to_regprocedure('worker_commit_simulation(text,text,text,character,jsonb,jsonb,jsonb)'))
        ) AS expected(oid) WHERE NOT has_function_privilege('algorithm_worker', oid, 'EXECUTE')
    ) THEN
        RAISE EXCEPTION 'algorithm_worker lacks an approved Worker function EXECUTE grant';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
        WHERE n.nspname = 'public' AND p.proname LIKE 'worker_%'
          AND has_function_privilege('algorithm_worker', p.oid, 'EXECUTE')
          AND p.oid NOT IN (
              to_regprocedure('worker_register_instance(text,text,text)'),
              to_regprocedure('worker_heartbeat_instance(text,text)'),
              to_regprocedure('worker_claim_next_job_for_worker(text,text,text,integer)'),
              to_regprocedure('worker_heartbeat_for_worker(text,text,text,text,integer)'),
              to_regprocedure('worker_cancellation_context(text,text,text)'),
              to_regprocedure('worker_report_event(text,text,text,jsonb)'),
              to_regprocedure('worker_complete_preflight(text,text,text,character,jsonb,character)'),
              to_regprocedure('worker_confirm_cancel(text,text,text)'),
              to_regprocedure('worker_fail_job(text,text,text,jsonb,boolean)'),
              to_regprocedure('worker_commit_simulation(text,text,text,character,jsonb,jsonb,jsonb)')
          )
    ) THEN
        RAISE EXCEPTION 'algorithm_worker has EXECUTE beyond the approved Worker function set';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
          AND (has_table_privilege('algorithm_worker', c.oid, 'SELECT')
               OR has_table_privilege('algorithm_worker', c.oid, 'INSERT')
               OR has_table_privilege('algorithm_worker', c.oid, 'UPDATE')
               OR has_table_privilege('algorithm_worker', c.oid, 'DELETE')
               OR has_table_privilege('algorithm_worker', c.oid, 'TRUNCATE')
               OR has_table_privilege('algorithm_worker', c.oid, 'REFERENCES')
               OR has_table_privilege('algorithm_worker', c.oid, 'TRIGGER'))
    ) THEN
        RAISE EXCEPTION 'algorithm_worker has direct application table privileges';
    END IF;
    IF EXISTS (
        SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public' AND c.relkind = 'S'
          AND (has_sequence_privilege('algorithm_worker', c.oid, 'USAGE')
               OR has_sequence_privilege('algorithm_worker', c.oid, 'SELECT')
               OR has_sequence_privilege('algorithm_worker', c.oid, 'UPDATE'))
    ) THEN
        RAISE EXCEPTION 'algorithm_worker has direct application sequence privileges';
    END IF;
    IF NOT has_table_privilege('web_api', 'schema_migrations', 'SELECT')
       OR has_table_privilege('web_api', 'schema_migrations', 'INSERT')
       OR has_table_privilege('web_api', 'schema_migrations', 'UPDATE')
       OR has_table_privilege('web_api', 'schema_migrations', 'DELETE') THEN
        RAISE EXCEPTION 'web_api must have only SELECT on schema_migrations';
    END IF;
END;
$$;

SELECT p.oid::regprocedure AS procedure,
       pg_get_userbyid(p.proowner) AS owner,
       p.prosecdef AS security_definer,
       coalesce(array_to_string(p.proconfig, ', '), '') AS configuration,
       has_function_privilege('public', p.oid, 'EXECUTE') AS public_execute,
       has_function_privilege('web_api', p.oid, 'EXECUTE') AS web_api_execute,
       has_function_privilege('algorithm_worker', p.oid, 'EXECUTE') AS algorithm_worker_execute
FROM pg_proc p JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = 'public' AND p.proname LIKE 'worker_%'
ORDER BY procedure;

SELECT c.relname,
       has_table_privilege('algorithm_worker', c.oid, 'SELECT') AS worker_select,
       has_table_privilege('algorithm_worker', c.oid, 'INSERT') AS worker_insert,
       has_table_privilege('algorithm_worker', c.oid, 'UPDATE') AS worker_update,
       has_table_privilege('algorithm_worker', c.oid, 'DELETE') AS worker_delete
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p')
ORDER BY c.relname;
