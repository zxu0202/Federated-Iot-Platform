# PostgreSQL Backup and Recovery Foundation

This runbook is a PostgreSQL-only operational foundation for OPS-005. It does
not authorise SQLite, a shared SQLite file, or a second Compose profile.

For PostgreSQL 18, the named `postgres-data` volume is mounted at
`/var/lib/postgresql`; the image uses its version-specific
`PGDATA=/var/lib/postgresql/18/docker` beneath that persistent volume. Do not
change the mount to the pre-PostgreSQL-18 `/var/lib/postgresql/data` location.

## M1 entry conditions

Do not run a backup or recovery drill until all of the following are true:

1. `scripts/Test-DeploymentConfig.ps1` or `scripts/test-deployment-config.sh`
   passes with a release-frozen image lock and local secret files.
2. The one-shot `migration` service and the Backend schema version check are available.
3. The Web/API readiness response reports `database_profile=postgres`, schema,
   dataset store, artifact store, reference profile, network binding, and Worker
   contract checks as required by `api_contract.md`.
4. No task is in `RUNNING`, `CANCELLING`, or `GENERATING_ARTIFACTS`. A graceful
   stop must leave an interrupted task in its persisted recovery state; it must
   never be changed by a shell script.

## Required backup contents

One backup set must contain all of the following at the same quiescent point:

- A PostgreSQL native logical or physical consistent backup.
- The `datasets` named volume.
- Only committed task artifacts and their `artifact_manifest.json` files.
- A manifest with release image references, schema version, configuration hash,
  per-file relative path, size, and SHA-256.

Do not copy a live database volume as a backup substitute. Do not include a
partially written `tmp/{attempt_id}` directory as a committed artifact.

## Recovery test procedure to implement at OPS-005

1. Preserve the original deployment and restore into a separately named Docker
   Compose project and fresh named volumes.
2. Restore the PostgreSQL backup before starting Web/API or Worker.
3. Restore datasets and committed artifacts to their matching volumes with the
   original relative paths and ownership model.
4. Start PostgreSQL, let the one-shot migration service complete its version
   gate, then start Web/API and Worker in the documented dependency order.
5. Compare database record counts, foreign-key checks, queue state, registered
   artifact references, file sizes, and SHA-256 values with the backup manifest.
6. Query one historical completed run through REST, download one artifact, and
   perform one replay export. A container health result alone is not recovery
   evidence.
7. Record image digest, schema version, backup manifest hash, restore target,
   command output, and any failed checks in the recovery report.

## Upgrade and rollback guard

Before a schema migration, create and verify a backup set. If migration or
application readiness fails, stop the release, retain the failed diagnostics,
and restore the verified previous image and database state. Completed runs are
immutable. Running work follows the Worker lease and `FAILED_RECOVERABLE`
contract; scripts must not update task rows directly.
