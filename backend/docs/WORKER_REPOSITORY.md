# PostgreSQL Worker Repository

The Algorithm Worker uses PostgreSQL functions only. It has no table grants,
no HTTP endpoint, and no SQLite adapter. Every task has one Worker lease and a
simulation task always contains the frozen generic Agent collection
`1/EARLY`, `2/MIDDLE`, and `3/LATE`.

## Role boundary

`platform_worker_repository_owner` is a dedicated `NOLOGIN` PostgreSQL role.
It owns every Worker-callable `SECURITY DEFINER` function and is never granted
to `web_api` or `algorithm_worker`. `platform_migrator` is the isolated
migration-service login. It may be a member of the NOLOGIN owner role and of
the legacy `web_api` owner only to perform the controlled ownership upgrade.
The running Web/API and Algorithm Worker containers never receive the
`platform_migrator` credential.

The deployment initializer must revoke `CREATE` on schema `public` from
`web_api` before the runtime service starts. The migration service runs:

```text
web-api migrate up
```

with `IOT_MIGRATION_DATABASE_URL` for `platform_migrator`. `web-api serve`
uses `IOT_DATABASE_URL` for `web_api` and performs only a read-only schema
checksum gate; it never applies a migration.

For an existing database where `000001_initial` was applied by `web_api`, stop
both runtime services and have a local PostgreSQL administrator run
`scripts/prepare_000002_owner_upgrade.sql` once before starting the dedicated
migration service. The bootstrap reassigns existing `web_api` objects to
`platform_migrator`, transfers `public` schema ownership to that migration
role, and grants only the migration service permission to set the final
NOLOGIN owner role. It must not be run by either runtime credential.

## Worker calls

All calls use positional PostgreSQL parameters. The Worker supplies generated
opaque `attempt_id` and lease-token values; a lease token must be at least 16
characters. All timestamps are database timestamps.

| Function | Arguments | Result | Boundary |
| --- | --- | --- | --- |
| `worker_register_instance` | `worker_id`, `worker.task.v1`, `worker_version` | timestamp | Upserts durable Worker registration. |
| `worker_heartbeat_instance` | `worker_id`, `worker.task.v1` | boolean | Refreshes an idle Worker observation. |
| `worker_claim_next_job_for_worker` | `worker_id`, `attempt_id`, `lease_token`, `lease_seconds` | one row or none | FIFO `FOR UPDATE SKIP LOCKED` claim; registration must be fresh. |
| `worker_heartbeat_for_worker` | `worker_id`, `job_id`, `attempt_id`, `lease_token`, `lease_seconds` | boolean | Renews observation and lease; emits persistent heartbeat facts. |
| `worker_cancellation_context` | `job_id`, `attempt_id`, `lease_token` | one row or none | Returns cancellation intent and lease validity. |
| `worker_report_event` | `job_id`, `attempt_id`, `lease_token`, `worker.event.v1` JSON | boolean | Persists Worker event and simulation SSE projection in one transaction. |
| `worker_recover_expired_leases` | none | recovered job count | Web/API-only persistent recovery; serializes with claims and never retries automatically. |
| `worker_complete_preflight` | lease identity, input SHA-256, summary JSON, summary SHA-256 | boolean | Marks only the matching `VALIDATING` dataset `VALID`. |
| `worker_confirm_cancel` | lease identity | boolean | Commits a persisted cancellation. |
| `worker_fail_job` | lease identity, error JSON, recoverable | boolean | Commits a stable failed terminal state. |
| `worker_commit_simulation` | lease identity and verified artifact metadata | boolean | Records only a previously atomically committed artifact set. |

The claim function injects the attempt identity and exact controlled output
directory before returning its strict `worker.task.v1` envelope:

- simulation: `runs/<run_id>/tmp/<attempt_id>`;
- preflight: `datasets/<dataset_id>/preflight/tmp/<attempt_id>`.

The Worker treats a false terminal or heartbeat result, an empty cancellation
context, or a database error as lost lease and stops work without publishing a
successful terminal state. `CANCELLING` is observed through the persisted
cancellation context; no process-local cancellation signal is authoritative.

Web/API calls `worker_recover_expired_leases` before serving and at the bounded
heartbeat interval. The function holds the same scheduler row lock as a Worker
claim, terminalizes each expired `RUNNING`/`CANCELLING` job as
`FAILED_RECOVERABLE` with `LEASE_LOST`, emits simulation state/queue events,
and changes an expired preflight dataset from `VALIDATING` to `INVALID` with
`PREFLIGHT_FAILED` / `LEASE_LOST`. It clears the lease identity, so every old
attempt write is rejected. A live lease prevents every later Worker claim.

## Readiness

`GET /api/v1/health/ready` reports the newest persisted compatible Worker as
`ok`, `stale`, or `not_observed`. The observation is fresh for the smaller of
three heartbeat intervals and one lease duration. Absence/staleness remains an
advisory readiness check so an idle Worker can start after Web/API; a database
query failure is `failed` and makes the endpoint unavailable.

## ACL verification

Run the following through a local administrator or migration-service evidence
session after migration. It must show the NOLOGIN owner, `prosecdef = true`, a
fixed `search_path`, no PUBLIC execute privilege, and only the documented
`algorithm_worker` execute grants.

```sql
SELECT p.proname,
       pg_get_function_identity_arguments(p.oid) AS arguments,
       owner_role.rolname AS owner,
       owner_role.rolcanlogin,
       p.prosecdef,
       p.proconfig,
       has_function_privilege('PUBLIC', p.oid, 'EXECUTE') AS public_execute,
       has_function_privilege('algorithm_worker', p.oid, 'EXECUTE') AS worker_execute
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
  )
ORDER BY p.proname, arguments;

SELECT table_name, privilege_type
FROM information_schema.role_table_grants
WHERE grantee = 'algorithm_worker'
  AND table_schema = 'public'
  AND table_name IN ('worker_jobs', 'worker_instances', 'worker_job_events')
ORDER BY table_name, privilege_type;
```

The second query must return no rows. These checks are local-only operational
evidence and do not publish data or artifacts.

## Clean-deployment verification

After 06 has started a fresh local PostgreSQL volume, run `web-api migrate up`
only in the migration service, then start Web/API with its `web_api`
credential. The repository includes a local-only executable check:

```text
MIGRATION_DATABASE_URL=<platform_migrator URL> \
WEB_API_DATABASE_URL=<web_api URL> \
WORKER_DATABASE_URL=<algorithm_worker URL> \
WEB_API_READY_URL=http://127.0.0.1:8080/api/v1/health/ready \
scripts/verify_worker_repository_acl.sh
```

It verifies the dedicated function owner and ACLs, the read-only schema gate
under the real Web/API credential, the absence of `public` schema `CREATE` for
that credential, direct `worker_jobs` SELECT denial for the real Worker
credential, and the running readiness endpoint. It is intentionally not run
against a non-disposable production database because the clean-volume setup is
owned by deployment verification.

`fixtures/worker/lease-recovery-preflight.v1.sql` is the deterministic local
PostgreSQL fixture for the expired-preflight path. It rolls back its setup and
asserts the recovered count, Worker job state, and dataset terminal state.
