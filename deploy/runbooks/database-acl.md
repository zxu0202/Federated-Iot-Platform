# PostgreSQL migration identity and ACL gate

This runbook applies only to the M1 PostgreSQL deployment. It does not
authorise SQLite, a shared database login, or a direct Worker table connection.

## Required identities

| Role | Login | Intended authority |
|---|---:|---|
| `platform_migrator` | Yes | The one-shot migration service only; NOINHERIT and no superuser/role/database administration attributes |
| `platform_worker_repository_owner` | No | Controlled owner of every `SECURITY DEFINER` Worker Repository function |
| `web_api` | Yes | Runtime Web/API queries only; no schema `CREATE` and no function ownership |
| `algorithm_worker` | Yes | Exact Worker Repository function `EXECUTE` only; no application-table or sequence privilege |

The PostgreSQL initialisation script makes `platform_migrator` the owner of the
`public` schema. It revokes `CREATE` from `PUBLIC`, both runtime roles, and the
controlled owner role. The migrator is a NOINHERIT member of the controlled
owner role, so migration SQL must not assume that owner privileges are inherited
after an object transfer. It must perform GRANT/REVOKE operations before the
ownership transfer, or use an explicitly tested `SET ROLE` sequence.

The init script has no broad default grant to `web_api`; it only removes default
`PUBLIC EXECUTE` from functions created by the migrator. `000002` must grant
the minimum required Web/API table, sequence, and `schema_migrations` SELECT
privileges explicitly, then grant the approved Worker functions exactly.

## Backend prerequisite for clean-deploy execution

The current deploy topology passes a local Docker secret through these fields:

```text
IOT_MIGRATION_DATABASE_HOST=postgres
IOT_MIGRATION_DATABASE_PORT=5432
IOT_MIGRATION_DATABASE_NAME=<POSTGRES_DB>
IOT_MIGRATION_DATABASE_USER=platform_migrator
IOT_MIGRATION_DATABASE_PASSWORD_FILE=/run/secrets/migrator_db_password
IOT_MIGRATION_DATABASE_SSLMODE=disable
```

Backend must build `MigrationDatabaseURL` from that structured input using the
same safe password-file parsing and URL encoding as the runtime database
configuration. It must retain the explicit `web-api migrate up` command and
must not run migrations from `web-api serve`. A raw migration DSN in `.env` or
a Compose environment variable is not acceptable because it exposes a password.

`000001` creates objects under `platform_migrator` on a clean volume. `000002`
must retain all of the following:

1. `platform_worker_repository_owner` owns each Worker Repository
   `SECURITY DEFINER` function, including the legacy terminal functions and
   `emit_queue_position_events`.
2. Each such function pins `search_path = pg_catalog, public`.
3. `PUBLIC` has no `EXECUTE`; `algorithm_worker` receives only the exact
   registration, liveness, claim, cancellation, event, preflight-completion,
   cancellation-confirmation, failure, and simulation-commit functions.
4. `algorithm_worker` has no direct table or sequence permission.
5. `web_api` receives only explicit runtime grants and exactly `SELECT` on
   `schema_migrations`.

Run an isolated clean deployment after this Backend prerequisite has been
integrated and tested. The Compose dependency remains fail-closed until then.

## Clean-deploy evidence procedure

1. Preserve the existing local deployment. Do not remove, rename, or reuse its
   `postgres-data` volume for ACL acceptance evidence.
2. Use a new Compose project name and its own empty volumes. Supply four
   distinct local secret files and a separately prepared deployment `.env`.
3. Start the isolated project only after release/build and Backend gates permit
   it. The one-shot `migration` service must reach exit code zero before
   Web/API starts.
4. Run `scripts/Test-DatabaseAcl.ps1` or `scripts/test-database-acl.sh` for the
   isolated project. Retain its output as local test evidence.
5. Confirm the migration container has stopped successfully and the three
   long-running containers are healthy. The ACL gate uses the local mounted
   service secret files without printing them: `algorithm_worker` direct
   `SELECT` and `worker_recover_expired_leases()` must be denied, while the
   `web_api` recovery call must succeed inside a transaction that is rolled
   back.

## Existing-volume restriction

The current local volume predates this gate: its objects were created by
`web_api`, and its initial migration has no explicit grants for a migrator-owned
schema. It is therefore not a clean-deploy ACL proof. Do not delete it.

Any future in-place conversion requires a separately approved backup, an
explicit administrator-reviewed ownership-transfer plan for the existing
`schema_migrations`, tables, sequences, functions, and triggers, and the same
ACL evidence gate afterward. A one-shot migration service must not be pointed
at that volume until the conversion plan is approved.
