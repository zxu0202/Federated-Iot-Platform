# PostgreSQL-only Deployment Foundation

This directory owns the M1 DevOps implementation for the first S1 closed loop.
It deliberately defines one Docker Compose topology only:

| Service | Host port | Purpose |
|---|---:|---|
| `migration` | None | One-shot PostgreSQL schema gate; exits successfully before runtime services start |
| `web-api` | All host IPv4 interfaces on `HOST_API_PORT` by default | Static SPA, REST, SSE, and health endpoints |
| `algorithm-worker` | None | One generic Worker process; a task contains Agent 1, Agent 2, and Agent 3 internally |
| `postgres` | None | PostgreSQL control database |

There is no SQLite Compose file, no fixed `agent-1`, `agent-2`, or `agent-3`
service, and no additional long-running proxy, queue, cache, or monitoring
service. `migration` is not an application runtime or a Worker; it uses the
same local Web/API image only to execute `web-api migrate up` once.

Only `web-api` joins the non-internal `iot_edge` bridge used for the published
host port. All long-running services join or use `iot_internal` as required,
while PostgreSQL and Algorithm Worker remain exclusively on that internal
network.

PostgreSQL 18 uses `PGDATA=/var/lib/postgresql/18/docker`; the named volume is
therefore mounted at `/var/lib/postgresql`, matching the official image volume
boundary and retaining the version-specific data directory.

## Release-freeze gate

`versions.release-freeze.yaml` is intentionally marked
`release-freeze-required`. Docker Desktop now passes the current-device M1
functional readiness profile. The six base images and the two `1.0.0-m1`
application images are present locally for `linux/amd64`; their local image IDs,
component labels, and container smoke checks are recorded in
`release-evidence/m1-local-image-builds.json`. Application release references,
image SBOMs, `linux/arm64` application builds, Compose live-driver evidence, and
release QA remain open.
`scripts/Test-DeploymentConfig.ps1` and
`scripts/test-deployment-config.sh` reject startup until an authorised operator
records exact tagged image references and verified SHA-256 manifest-list digests
for PostgreSQL, every build/runtime base image, and both built application
images. The `1.0.0-m1` output names in `.env.example` are local candidate names only;
they are not deployable release references.

The display brand `zx/federated-iot-platform:latest` is retained in the config
template. It is not a reproducible release reference. A release manifest must
record an immutable application image reference, source revision, SBOM SHA-256,
and the verified platform descriptors for `linux/amd64` and `linux/arm64`.

## Configure a controlled host

Copy the templates without committing the resulting files:

```powershell
Copy-Item .env.example .env
Copy-Item config\platform.example.yaml config\platform.yaml
```

```sh
cp .env.example .env
cp config/platform.example.yaml config/platform.yaml
```

Create four distinct local secret files and point the four `*_PASSWORD_SOURCE`
settings in `.env` to them. Each file contains one password and is mounted as a
Docker Compose secret. Passwords never belong in `.env`, `platform.yaml`, an
image layer, Docker environment, or a log. The PostgreSQL initialisation script
creates a `platform_migrator` login, the dedicated NOLOGIN
`platform_worker_repository_owner`, and separate `web_api` and
`algorithm_worker` runtime logins. The four password source paths must be
distinct. Role names are fixed by the DB ACL contract.

For a new local workstation, the following helper creates the four files with
cryptographically random values, refuses to overwrite existing secrets, and
never prints their contents:

```powershell
.\scripts\Initialize-LocalSecrets.ps1
```

For an existing three-secret deployment, the same helper creates only the
missing `platform_migrator_db_password.txt` file. It never reads, replaces, or
prints existing values. When all four files already exist, it reports a no-op.

For a release build, keep the active `.env`, secret files, and populated
`platform.yaml` outside the source checkout and pass their paths to the start
scripts. The Web/API image build context spans `code/` so it can compile the
Backend and SPA together. A root `.dockerignore` owned by the architecture
task must exclude deployment secret/config material before release builds; this
deployment scope does not modify that cross-owner file.

Set `database.profile: postgres`. The deployment validator rejects `sqlite`.
For `zl` and `sd`, keep `validation_enabled: false` until an operator supplies
the approved standard reference, unit, minimum, maximum, expected sampling
period, and tolerance. If validation is enabled while any of those values is
`null`, readiness must fail.

By default, `PLATFORM_BIND_ADDRESS=0.0.0.0` and Compose publishes
`0.0.0.0:${HOST_API_PORT}:8080`. Web/API always listens on `0.0.0.0:8080`
inside the isolated container network. This default exposes the Web/API port on
all host IPv4 interfaces; it does not publish PostgreSQL or Worker ports.

To intentionally restrict host ingress, set `PLATFORM_BIND_ADDRESS` to an IPv4
address assigned to the host, or provide `-BindAddress` / `--bind-address`.
`-BindInterface` / `--bind-interface`, `PLATFORM_BIND_INTERFACE`, and
`PLATFORM_CANDIDATE_INTERFACES` remain explicit convenience inputs that resolve
to a host-assigned IPv4 address. With no explicit address or interface input,
the scripts do not inspect or select a network adapter and retain the
all-interface default. `network.host_binding` records that operator decision
under deployment change control. `hostname`, `public_base_url`, and
`allowed_hosts` are declarations only; these scripts never modify DNS or the
operating system hosts file.

Examples of explicit startup selection:

```powershell
.\scripts\Start-Platform.ps1 -BindAddress <host-assigned-ipv4>
.\scripts\Start-Platform.ps1 -BindInterface WLAN
```

```sh
./scripts/start-platform.sh --bind-address <host-assigned-ipv4>
./scripts/start-platform.sh --bind-interface eth0
```

Database traffic never uses the selected host address. Web/API and Worker reach
the separate PostgreSQL container as `postgres:5432` on `iot_internal`, and the
database has no published host port. `127.0.0.1` inside Web/API or Worker would
refer to that same application container, not to PostgreSQL.

## Connected public source build

The public repository supports a separate connected-source image path for a
clean Git clone. It does not require the ignored Backend vendor tree, Frontend
npm cache, or Worker wheelhouse. Use:

```powershell
.\scripts\Build-ConnectedSourceImages.ps1
.\scripts\Start-Platform.ps1 `
  -ConnectedSourceBuild `
  -EnvironmentFile .\.env.connected.runtime `
  -ConfigFile .\config\platform.yaml
```

Run both commands from `deploy/` as shown. The
[connected source guide](runbooks/connected-source-build.md) also provides
repository-root forms. The build requires a clean committed revision,
uses checksum-pinned dependency inputs, records the BuildKit Worker manifest
digest, and creates dedicated source-bound `1.0.0-m1-connected-<source-sha>`
tags under an isolated Compose project name. It does not modify the release
lock or claim equivalence with an official frozen image.

## Offline release build contract

`Dockerfile.web-api` and `Dockerfile.algorithm-worker` are multi-stage and
offline by construction. They expect the following inputs from their owners:

- `backend/vendor/`, `backend/go.mod`, and the Go command at `./cmd/web-api`.
- `frontend/package-lock.json`, a pre-populated `frontend/.npm-cache`, and a
  production `npm run build` output.
- `algorithm-worker/requirements.lock` with hashes, a local `wheelhouse/`, and
  Python package source at `algorithm-worker/src`.

The public Git repository intentionally omits `backend/vendor/`,
`frontend/.npm-cache`, and wheel binaries. Before a disconnected release build,
an approved networked staging environment must restore those inputs. Prepare
the Backend vendor tree with exactly Go 1.23.0 and the checked-in module files:

```powershell
Set-Location backend
$env:GOTOOLCHAIN = 'local'
go mod download
go mod verify
go mod vendor
```

Use the staging environment's approved `GOPROXY` and checksum database policy.
Then disconnect the release environment and run the offline supply-chain
verifier. Stop if the regenerated `vendor/modules.txt`, canonical vendor-tree
hash, or dependency manifest differs from the frozen release identity.

The Dockerfiles use `go build -mod=vendor` and
`pip install --no-index --require-hashes`. The frontend build uses:

```text
npm ci --offline --ignore-scripts --cache ./npm-cache --registry=https://registry.npmmirror.com
```

The explicit npm registry is the registry identity recorded by the
pre-populated offline cache; it does not permit network access because
`--offline` remains required. Missing offline inputs fail instead of
downloading dependencies. The release-frozen runtime bases must be
Debian-compatible because the images create non-root users with `groupadd` and
`useradd`.

Backend must expose the normal HTTP health endpoints from the frozen API
contract and the non-network container command:

```text
web-api healthcheck --kind=live --config /etc/federated-iot/platform.yaml
```

Worker must provide:

```text
python -m federated_iot_worker.healthcheck \
  --config /etc/federated-iot/platform.yaml \
  --max-heartbeat-age-seconds 30
```

These interfaces are required for the Compose health checks. The Worker must
not claim a task before the migration gate and Web/API readiness prerequisites
are met. Web/API readiness remains the contract authority for PostgreSQL schema,
storage, field-standard, network-binding, and Worker contract status.

## Database migration and ACL gate

The `migration` service is the only Compose service allowed to execute
`web-api migrate up`. It receives the `platform_migrator` secret and joins only
`iot_internal`. `web-api` waits for its successful completion and uses only the
`web_api` secret at runtime; `algorithm-worker` waits for both migration and
Web/API health. Neither runtime role may create objects in `public`.

`platform_worker_repository_owner` is a controlled NOLOGIN role. The migrator
is its NOINHERIT member so it can transfer approved Worker Repository functions
to that owner. `algorithm_worker` receives no application-table or sequence
grant. `PUBLIC` receives no function execute grant; Worker receives only the
precise function calls listed in `postgres/verify/acl-assertions.sql`. The
initialisation script deliberately gives no default Web/API DML or sequence
grant: `000002` must grant each required runtime privilege explicitly.

The Compose migration service deliberately supplies structured, secret-file
inputs (`IOT_MIGRATION_DATABASE_HOST`, `PORT`, `NAME`, `USER`,
`PASSWORD_FILE`, `SSLMODE`) rather than a password-bearing URL. Do not put
`IOT_MIGRATION_DATABASE_URL` in `.env`. Until Backend parses those structured
inputs into an encoded migration connection URL, the migration container must
fail closed and clean-deploy validation is blocked. See
`runbooks/database-acl.md` for the exact Backend integration requirements and
legacy-volume restriction.

## Parameter constraints

`../backend/config/parameter-constraints.v1.json` is the authoritative
`parameter-constraints.v1` schema. The deployment copy at
`config/parameter-constraints.v1.json` is byte-for-byte identical and is the
default local bind source. It contains exactly 69 named parameter paths: 67
editable paths and two fixed S1 topology paths, `split.agent_count` and
`global_surrogate.leave_one_out`. Every rule declares `type`, `editable`,
`nullable`, `minimum`, `maximum`, and `allowed_values`.

Shared leaf rules also define the type and constraint boundary for the matching
Agent sparse override. The Backend resolves the final Agent override JSON path;
deployment does not introduce an Agent-only parameter surface.

Set `PARAMETER_CONSTRAINTS_SOURCE` to an exact local copy of the authoritative
JSON file. Compose mounts it read-only at
`/etc/federated-iot/parameter-constraints.v1.json` in Web/API and exposes that
absolute path through `IOT_PARAMETER_CONSTRAINTS_FILE`. Both deployment
validators reject a missing file, invalid schema shape, invalid fixed items,
wrong 69/67/2 counts, non-matching semantic content, SHA-256 mismatch with the
Backend source, or a non-read-only Compose mount.

## Start, stop, status, and logs

Before a local Docker Desktop deployment, run the read-only Windows readiness
check. It verifies Docker client/server access, Compose v2, Linux containers,
the resource budget required by the current-device M1 functional profile
(4 CPUs and 3 GiB), the
local bridge network, IPv4 forwarding, and available host IPv4 candidates. It
does not build, pull, create, start, stop, or remove anything:

```powershell
.\scripts\Test-DockerDesktopReadiness.ps1
```

Use the Docker CLI from `PATH` by default. If it is installed elsewhere,
replace `docker` with the absolute path to the executable:

```powershell
$DockerExecutable = 'docker'
.\scripts\Test-DockerDesktopReadiness.ps1 -DockerExecutable $DockerExecutable
```

The same optional `-DockerExecutable` argument is available on the Windows
start and stop scripts. It accepts an existing absolute `docker.exe` path or a
resolvable `docker` command. The scripts prepend only that executable directory
to the Docker child-process PATH so `docker-credential-desktop` remains
discoverable; they restore the calling PowerShell PATH immediately afterwards
and never modify the system or user environment variables.

Only after QA closes the M1 integration gates, the release lock, config, secret
files, and offline images are ready may an operator continue with the following
deployment commands:

```powershell
.\scripts\Test-DeploymentConfig.ps1
.\scripts\Start-Platform.ps1
.\scripts\Start-Platform.ps1 -Build
.\scripts\Stop-Platform.ps1
docker compose --env-file .env -f compose.postgres.yaml ps
docker compose --env-file .env -f compose.postgres.yaml logs --tail 200
```

Use the same Docker executable value for every Windows Docker operation:

```powershell
$DockerExecutable = 'docker'
.\scripts\Start-Platform.ps1 -DockerExecutable $DockerExecutable
.\scripts\Stop-Platform.ps1 -DockerExecutable $DockerExecutable
& $DockerExecutable compose --env-file .env -f compose.postgres.yaml ps
& $DockerExecutable compose --env-file .env -f compose.postgres.yaml logs --tail 200
```

```sh
sh scripts/test-deployment-config.sh
sh scripts/start-platform.sh
sh scripts/start-platform.sh --build
sh scripts/stop-platform.sh
docker compose --env-file .env -f compose.postgres.yaml ps
docker compose --env-file .env -f compose.postgres.yaml logs --tail 200
```

The start scripts run static validation before they invoke Docker Compose. They
default to the `0.0.0.0` host mapping, accept a host-assigned IPv4 only as an
explicit ingress restriction, never alter host networking, and never create an
extra service. `pull_policy: never` preserves the offline runtime boundary. The
`-Build` or `--build` option only works when all frozen base images and offline
dependency caches are already loaded locally.

## Volumes, limits, and lifecycle foundation

Compose creates exactly three named persistent volumes:

- `postgres-data`: only PostgreSQL data.
- `datasets`: Web/API creates immutable input files and preflight directories;
  Worker mounts the volume writable but filesystem ACLs restrict it to leased
  preflight attempt trees. The Worker can read, but cannot modify, `source.csv`.
- `artifacts`: Worker writes task temporary and committed directories; Web/API
  mounts it read-only.

The artifact namespace root is `/var/lib/iot`. Database and Worker metadata use
canonical relative paths beginning with `runs/`, such as
`runs/{run_id}/committed/{artifact_name}`. Web/API therefore receives
`IOT_ARTIFACT_ROOT=/var/lib/iot` and only the `artifacts` subvolume at
`/var/lib/iot/runs:ro`; joining the namespace root and canonical path resolves
to the mounted committed file without duplicating `runs/`. Worker shares the
same volume at `/var/lib/iot/runs` for atomic temporary-to-committed publishing.
The required committed inventory is 12 files: the 11 non-self items in API
contract section 13.1 plus `artifact_manifest.json` itself.

Web/API runs as UID 10001 and Worker as UID 10002, with a shared non-root group
for the artifact volume. Both application root filesystems are read-only, drop
all Linux capabilities, and use a bounded `/tmp`. PostgreSQL does not publish a
port. Resource ceilings for the current 8-CPU/3.7-GiB Docker Desktop allocation
default to 0.5 CPU/512 MiB for Web/API, 2 CPU/1536 MiB for the Worker, and
0.5 CPU/768 MiB for PostgreSQL; Worker numerical-library thread counts are
bounded at two. The three long-running service limits total 3 CPUs and 2816 MiB,
leaving capacity for Docker overhead. During startup, the one-shot migration
service is limited to 0.25 CPU and 256 MiB and Web/API has not started yet. This
current-device profile is for M1 functional validation; it does not replace
full-data performance, resource, or 72-hour RC evidence. Docker `local` logging
rotates at 20 MiB × 5 files by default.

The Algorithm Worker Pool has a fixed M1 capacity of one. Compose declares one
`algorithm-worker` service with `deploy.replicas: 1`, the platform configuration
declares `worker_pool_capacity: 1`, and the start scripts do not accept a scale
override. This does not create separate Agent 1/2/3 services or authorise
concurrent Workers.

PostgreSQL health requires both `pg_isready` and the role-initialisation marker
written after the service-role transaction succeeds. This prevents Web/API from
starting against the temporary PostgreSQL server used during first-run image
initialisation.

After a clean isolated deployment succeeds, run the ACL evidence gate. It is
read-only apart from the harmless role-scoped function invocation used to prove
that an approved Worker function can execute; it neither deletes a volume nor
changes data:

```powershell
.\scripts\Test-DatabaseAcl.ps1 -ProjectName federated-iot-platform-acl
```

```sh
COMPOSE_PROJECT_NAME=federated-iot-platform-acl sh scripts/test-database-acl.sh
```

The gate records role attributes, function owner/`SECURITY DEFINER`/fixed
`search_path`, exact EXECUTE grants, direct table privilege flags, an actual
denied `algorithm_worker` `SELECT` and recovery-function call, plus a
service-credential `web_api` recovery-function call inside a rolled-back
transaction.

All services use a 30-second stop grace period and `unless-stopped`. A task
failure must be represented through the backend/Worker state contract, not by
restarting the whole stack. OPS-004 will validate liveness, readiness, graceful
stop, task recovery, and restart behaviour against real binaries.

## Backup, recovery, upgrade, and rollback

The PostgreSQL-only OPS-005 foundation is in
`runbooks/backup-recovery.md`. It specifies a native consistent PostgreSQL
backup plus datasets, committed artifacts, and a hash manifest; copying a live
database volume is not accepted. Restore testing must use fresh volumes and
verify record counts, foreign keys, references, sizes, hashes, REST artifact
download, and replay export. Migration/restore automation is intentionally
deferred until Backend delivers migrations and artifact integrity checks.

The non-destructive recovery-test entry verifies the required running services
and the contract readiness surface before an isolated drill:

```powershell
.\scripts\Test-PostgresRecoveryPrerequisites.ps1 -BaseUrl http://192.0.2.10:8080
```

```sh
sh scripts/test-postgres-recovery-prerequisites.sh http://192.0.2.10:8080
```

Replace the documentation address with the host address selected by the start
script. The entry does not create, replace, or delete volumes; the OPS-005
drill is the first action allowed to restore into fresh volumes.

No upgrade or rollback script may mutate completed task results, delete user
data, or repair task states directly. A release must back up first, then stop on
migration/readiness failure and use the tested recovery path.

Release evidence and offline-package inputs are documented in
`runbooks/release-freeze.md`; generated SBOM files belong under `sbom/`.
The local-only offline package format and the manual Docker Hub/Zenodo
distribution procedures are documented in `offline/package/README.md` and
`runbooks/offline-distribution.md`. No automated distribution action is
permitted.

## Public source and code-cut hygiene

The public-source candidate contains only reproducible deployment source:
Compose, Dockerfiles, non-secret templates, selected scripts, English runbooks,
version manifests, and static OCI descriptor evidence. `.runtime.env`, populated
configuration, secrets, image archives, package output, machine-local release
evidence, test data, logs, caches, and editor workspace files remain local
and are excluded by Git ignore rules.

`runbooks/code-cut-deployment-manual.md` is the detailed future operator manual
for locked local builds, dual local package layout, offline receiving-host
validation, Docker Compose operations, backup/recovery, and manual Docker Hub
or Zenodo distribution. It is not release approval. Do not generate packages,
publish source, push images, or upload archives while the release gate remains
open.

## Backend offline Go verification

Offline Backend verification requires genuinely generated `go.sum`, `vendor/`,
and `vendor/modules.txt`. The vendor tree is a local release input and is not
tracked by the public Git repository. The deployment toolchain binds that exact
snapshot with a canonical vendor-tree checksum, an offline-input checksum
manifest, and a CycloneDX dependency SBOM. The verification entry requires
exactly Go 1.23.0, sets `GOTOOLCHAIN=local`, `GOPROXY=off`, and `GOSUMDB=off`,
verifies the vendor tree, and then runs `go test -mod=vendor ./...` plus
`go build -mod=vendor ./cmd/web-api`. Optional module-cache archives have a
separate SHA-256 and run `go mod verify`.

See `offline/go/README.md` and run the platform-specific script in `scripts/`
only after the dependency material is restored by an approved staging process
and delivered to the disconnected release environment.
