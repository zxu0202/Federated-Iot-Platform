#!/usr/bin/env sh
set -eu

: "${MIGRATION_DATABASE_URL:?MIGRATION_DATABASE_URL is required}"
: "${WEB_API_DATABASE_URL:?WEB_API_DATABASE_URL is required}"
: "${WORKER_DATABASE_URL:?WORKER_DATABASE_URL is required}"
: "${WEB_API_READY_URL:?WEB_API_READY_URL is required}"

psql "$MIGRATION_DATABASE_URL" --set=ON_ERROR_STOP=1 --tuples-only --no-align <<'SQL' | grep -qx 't'
SELECT bool_and(owner_role.rolname = 'platform_worker_repository_owner')
   AND bool_and(NOT owner_role.rolcanlogin)
   AND bool_and(p.prosecdef)
   AND bool_and(COALESCE(array_to_string(p.proconfig, ','), '') LIKE '%search_path=pg_catalog, public%')
   AND bool_and(NOT has_function_privilege('PUBLIC', p.oid, 'EXECUTE'))
  FROM pg_proc AS p
  JOIN pg_namespace AS n ON n.oid = p.pronamespace
  JOIN pg_roles AS owner_role ON owner_role.oid = p.proowner
 WHERE n.nspname = 'public'
   AND p.proname IN (
       'worker_claim_next_job', 'worker_heartbeat', 'worker_report_stage',
       'worker_complete_preflight', 'worker_confirm_cancel', 'worker_fail_job',
       'worker_commit_simulation', 'worker_register_instance',
       'worker_heartbeat_instance', 'worker_claim_next_job_for_worker',
       'worker_heartbeat_for_worker', 'worker_cancellation_context',
       'worker_report_event', 'worker_recover_expired_leases'
   );
SQL

psql "$WEB_API_DATABASE_URL" --set=ON_ERROR_STOP=1 --tuples-only --no-align <<'SQL' | grep -qx 'f'
SELECT has_schema_privilege(current_user, 'public', 'CREATE');
SQL

psql "$WEB_API_DATABASE_URL" --set=ON_ERROR_STOP=1 --quiet <<'SQL'
SELECT checksum_sha256 FROM schema_migrations ORDER BY version;
SELECT count(*) FROM scheduler_control;
SELECT count(*) FROM worker_instances;
SQL

if psql "$WORKER_DATABASE_URL" --set=ON_ERROR_STOP=1 --quiet --command 'SELECT 1 FROM public.worker_jobs LIMIT 1;' >/dev/null 2>&1; then
    printf '%s\n' 'algorithm_worker unexpectedly has direct worker_jobs SELECT privilege.' >&2
    exit 1
fi

psql "$WORKER_DATABASE_URL" --set=ON_ERROR_STOP=1 --tuples-only --no-align <<'SQL' | grep -qx 'f'
SELECT has_table_privilege(current_user, 'public.worker_jobs', 'SELECT')
    OR has_table_privilege(current_user, 'public.worker_instances', 'SELECT')
    OR has_table_privilege(current_user, 'public.worker_job_events', 'SELECT');
SQL

curl --fail --silent --show-error "$WEB_API_READY_URL" >/dev/null
printf '%s\n' 'Worker Repository ACL verification passed.'
