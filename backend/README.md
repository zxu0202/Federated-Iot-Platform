# Web/API Backend

Version: **1.0.0-m1**

This module is the Go control plane for the Federated IoT Platform. It owns the
HTTP/SSE API, PostgreSQL persistence and migrations, dataset admission,
immutable parameter and simulation snapshots, queue/cancellation state, and
verified reads of committed Worker artifacts. It does not implement Algorithm
Core or a frontend.

## Architecture boundary

M1 is PostgreSQL-only. SQLite is an approved future adapter boundary, not an
implemented runtime option. Web/API admits work and projects durable state;
the Algorithm Worker performs preflight and simulation work through the narrow
PostgreSQL Worker Repository. A simulation uses the frozen three-Agent S1
topology and one Worker execution slot.

The runtime API is rooted at `/api/v1`. It exposes health, datasets,
configuration and parameter profiles, simulations, SSE, and read-only
artifact/result/replay endpoints. The OpenAPI contract is maintained outside
this module. Worker leases, lease tokens, storage paths, and database errors
are never API response fields.

## Build and test

The public Git source intentionally omits `vendor/`. Use Go 1.23.0 with the
checked-in `go.mod` and `go.sum`; module resolution uses the configured trusted
`GOPROXY` or a pre-populated module cache.

```powershell
$env:GOTOOLCHAIN = 'local'
go mod download
go mod verify
go test -count=1 -mod=readonly ./...
go build -mod=readonly ./cmd/web-api
```

Use a writable temporary `GOCACHE` when the default cache is unavailable.
Release preparation restores or generates the reviewed `vendor/` tree in an
approved staging environment. The frozen Docker and offline verification path
then keeps `GOPROXY=off`, `GOSUMDB=off`, and `-mod=vendor`.

## Commands and runtime separation

```text
web-api serve
web-api migrate up
web-api migrate down
web-api healthcheck --kind=live --config <path>
```

`serve` uses the lower-privilege `web_api` database credential. It performs a
read-only migration checksum gate and never applies migrations.
`migrate` requires the dedicated `platform_migrator` credential. The migration
service and runtime service must not share credentials. The healthcheck command
is local and non-network; readiness is the running service's HTTP endpoint.

## Configuration and secrets

`.env.example` is a public template only. Do not place credentials, API keys,
or private keys in it, source files, fixtures, or command lines. Set either
`IOT_DATABASE_URL` / `IOT_MIGRATION_DATABASE_URL`, or the corresponding
structured `IOT_*_DATABASE_HOST`, `NAME`, `USER`, `PORT`, `SSLMODE`, and
`PASSWORD_FILE` values. A password file is local runtime input and must not be
committed.

Important runtime settings include `IOT_HTTP_ADDRESS` (or `PORT`),
`IOT_DATASET_ROOT`, `IOT_ARTIFACT_ROOT`, `IOT_PARAMETER_CONSTRAINTS_FILE`, and
the immutable Algorithm/Worker runtime identity values. The default listener is
`0.0.0.0:8080`; deployments may explicitly provide a validated address.

`IOT_PARAMETER_CONSTRAINTS_FILE` must reference a complete local
`parameter-constraints.v1` JSON document. Missing or invalid constraints cause
stable readiness and CUSTOM-profile failures rather than a permissive fallback.

## Security and persistence boundaries

Worker database access is limited to approved `SECURITY DEFINER` functions
owned by the NOLOGIN `platform_worker_repository_owner` role. It has no direct
table access. Migrations preserve fixed `pg_catalog,public` search paths and
revoke PUBLIC execute privileges. Detailed migration, role, ACL, and recovery
guidance is in [docs/POSTGRES_OPERATIONS.md](docs/POSTGRES_OPERATIONS.md) and
[docs/WORKER_REPOSITORY.md](docs/WORKER_REPOSITORY.md).

Datasets and artifact reads remain rooted beneath configured local storage.
The service verifies containment, regular-file state, size, and SHA-256 before
streaming committed artifacts. Simulation snapshots and terminal data are
immutable; later CUSTOM profile aliases or versions cannot rewrite history.

## M1 scope and future work

M1 provides the first complete PostgreSQL closed loop: structural CSV import,
Worker preflight, admission, leasing, cancellation, durable events, history,
SSE, artifacts, results, alarms, replay, and export boundaries.

M2+ work requires a separate approved gate. It may consider an independently
validated SQLite adapter, expanded topology, or further integration work. This
module does not pre-implement those options.

## License

This module is distributed under the repository-level MIT License.
