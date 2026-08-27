# PostgreSQL Migration, Constraints, and Recovery

## Ownership and deployment profile

Backend exclusively owns the migration files in
`internal/storage/postgres/migrations`. The approved first closed loop uses
PostgreSQL only. This documentation does not define a SQLite setup, migration,
or recovery path.

The production image and database image must be fixed by exact version and
digest in the deployment manifest. The brand tag
`zx/federated-iot-platform:latest` is presentation metadata only; it is not an
acceptable reproducibility reference.

## Migration flow

1. Take a PostgreSQL-consistent backup of the current control database and the
   `datasets/` and `runs/*/committed/` local-volume trees.
2. Start the dedicated migration service with an offline dependency set and
   `IOT_MIGRATION_DATABASE_URL`, then execute `web-api migrate up`.
3. Start Web/API only with the lower-privilege `IOT_DATABASE_URL`; it verifies
   schema checksums read-only and does not apply migrations.
4. Verify `schema_migrations`, `scheduler_control`, the reference parameter
   profile, the identity mapping profile, and all required indexes/functions.
5. Run the admission, lease, cancellation, SSE, and recovery fixtures before
   promoting the database image.

`platform_worker_repository_owner` must be a `NOLOGIN` role that owns every
Worker `SECURITY DEFINER` function. `platform_migrator` is the only migration
service login and may hold the temporary ownership-upgrade memberships. Neither
`web_api` nor `algorithm_worker` may inherit the NOLOGIN owner role; the Worker
has function EXECUTE only and no table grant. See `WORKER_REPOSITORY.md` for
the required local ACL query.

If the old Web/API runtime role applied `000001_initial`, stop runtime
containers and have a local PostgreSQL administrator execute
`scripts/prepare_000002_owner_upgrade.sql` before `platform_migrator` applies
`000002`. This ownership bootstrap is required because `NOINHERIT` membership
does not grant automatic DDL ownership privileges after an object transfer.

Rollback is only safe before production data is admitted. The down migration
removes the control-plane tables, so operational rollback after acceptance must
instead restore the consistent database and volume backup.

## Control-plane constraints

- `simulations_single_execution_slot` permits only one RUNNING, CANCELLING, or
  GENERATING_ARTIFACTS simulation.
- `worker_jobs_single_execution_slot` permits one Worker lease across preflight
  and simulation jobs, preserving global FIFO and no preemption.
- `scheduler_control` fixes ten simulation waiting slots and a separate
  preflight queue capacity of four.
- Immutable profiles, mappings, snapshots, and terminal simulations are guarded
  by database triggers.
- `simulation_events` are append-only and bounded per run by the retention
  trigger; an expired SSE cursor is reset rather than silently replayed.
- Artifact metadata admits only safe relative paths and completed simulations
  require the committed artifact state.

## Backup and restore verification

1. Stop new writes and wait for Web/API transactions to drain.
2. Create a PostgreSQL-consistent database backup and record its SHA-256.
3. Copy datasets and committed run directories without changing their contents;
   record file-manifest hashes.
4. Restore to an isolated PostgreSQL instance and attach copies of the data
   volumes.
5. Compare dataset/run/artifact counts, dataset hashes, snapshot hashes, and
   manifest hashes. Inspect every completed run for its registered committed
   manifest and required artifacts.
6. Start Worker only after the comparison passes. Expired in-flight leases must
   enter `FAILED_RECOVERABLE`; S1 never silently reruns them.

No automated deletion is permitted while retention policy remains unfrozen.
