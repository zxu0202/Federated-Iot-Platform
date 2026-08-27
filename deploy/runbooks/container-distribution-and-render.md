# Container Deployment, Manual Distribution, and Render Assessment

## Purpose and authority boundary

This runbook describes the current PostgreSQL-only M1 Docker deployment, the
manual distribution process for Docker Hub and Zenodo, and the limits of running the
current topology on Render. It is an operational reference, not approval to
release a build.

All source, images, SBOMs, evidence, and offline packages remain local. An
automation must never run `docker push`, upload to Zenodo, publish a release, or
transmit credentials. An authorised human may perform the explicitly marked
manual distribution steps only after separate approval.

The current `versions.release-freeze.yaml` status is
`release-freeze-required`. No image is a release artifact until the lock is
frozen with verified immutable registry references, application SBOMs,
multi-platform evidence, offline-package validation, and release QA.

## Current Docker topology

The only supported M1 topology is defined in `compose.postgres.yaml`:

| Service | Runtime role | Network exposure |
| --- | --- | --- |
| `postgres` | PostgreSQL control database | `iot_internal` only; no host port |
| `migration` | One-shot schema gate using `platform_migrator` | `iot_internal` only |
| `web-api` | SPA, REST, SSE, and health surface | `iot_edge` and `iot_internal`; only host port |
| `algorithm-worker` | One generic S1 Worker | `iot_internal` only |

There is exactly one Worker (`deploy.replicas: 1` and
`WORKER_POOL_CAPACITY=1`). Agent 1, Agent 2, and Agent 3 execute inside that
Worker; they are not independent containers or services.

The named volumes have separate responsibilities:

| Volume | Owner/use | Required mount |
| --- | --- | --- |
| `postgres-data` | PostgreSQL data | `/var/lib/postgresql` in PostgreSQL |
| `datasets` | Dataset storage | `/var/lib/iot/datasets` in Web/API and Worker |
| `artifacts` | Worker-published task artifacts | Worker: `/var/lib/iot/runs`; Web/API: `/var/lib/iot/runs:ro` |

Database and Worker artifact metadata use paths beginning with `runs/`.
Web/API must keep `IOT_ARTIFACT_ROOT=/var/lib/iot`; it must not use
`/var/lib/iot/runs` as the namespace root, or the `runs/` prefix will be joined
twice.

## Local Docker deployment

### Preconditions

Before a start, an operator must have all of the following locally:

1. Docker Engine in Linux-container mode and the required host resource budget.
2. All exact image references already present locally. `pull_policy: never` is
   intentional.
3. A frozen release lock for a release deployment. An open lock is a hard stop.
4. An operator-owned environment file, `platform.yaml`, parameter constraints
   JSON, and four distinct secret files outside version control.
5. A verified backup and a tested rollback/recovery plan before any upgrade.

On Windows with a custom Docker Desktop installation, an operator may use:

```powershell
$DockerExecutable = 'docker'
.\scripts\Test-DockerDesktopReadiness.ps1 -DockerExecutable $DockerExecutable
```

This readiness check is read-only. It does not build, pull, create, start,
stop, or remove Docker objects.

### Operator environment file

Use an operator-owned file passed with `--env-file`; do not commit it. The
following entries show the deployment contract and intentionally omit secret
values:

```dotenv
# Every image must have an exact tag and immutable digest after release freeze.
POSTGRES_IMAGE=docker.io/library/postgres:<version>@sha256:<manifest-digest>
WEB_API_IMAGE=<registry>/<namespace>/federated-iot-platform:<release-tag>@sha256:<manifest-digest>
ALGORITHM_WORKER_IMAGE=<registry>/<namespace>/federated-iot-platform-worker:<release-tag>@sha256:<manifest-digest>

BUILD_VERSION=<release-version>
FRONTEND_VERSION=<release-version>
BACKEND_VERSION=<release-version>
ALGORITHM_VERSION=<release-version>
WORKER_VERSION=<release-version>
VCS_REF=<verified-source-revision>

# Default Web/API host publishing is all IPv4 interfaces. Set a host-assigned
# IPv4 address only to deliberately restrict ingress.
PLATFORM_BIND_ADDRESS=0.0.0.0
HOST_API_PORT=8080

POSTGRES_DB=federated_iot
POSTGRES_ADMIN_USER=platform_admin
MIGRATOR_DB_USER=platform_migrator
WEB_API_DB_USER=web_api
WORKER_DB_USER=algorithm_worker
WORKER_REPOSITORY_OWNER=platform_worker_repository_owner

# The four paths must be distinct one-line local secret files.
POSTGRES_ADMIN_PASSWORD_SOURCE=<local-path>
MIGRATOR_DB_PASSWORD_SOURCE=<local-path>
WEB_API_DB_PASSWORD_SOURCE=<local-path>
WORKER_DB_PASSWORD_SOURCE=<local-path>

PLATFORM_CONFIG_PATH=<local-platform-yaml-path>
PARAMETER_CONSTRAINTS_SOURCE=<local-parameter-constraints-json-path>
WORKER_INSTANCE_ID=algorithm-worker-1
WORKER_NUMERIC_THREADS=2
```

`parameter-constraints.v1.json` is configuration, not a secret, but it must
remain an exact copy of the Backend authoritative schema. Compose mounts it
read-only at `/etc/federated-iot/parameter-constraints.v1.json` and provides
that path through `IOT_PARAMETER_CONSTRAINTS_FILE`.

The four password files are Docker secrets. Do not place a password in an
environment variable, image label, image layer, Compose file, log, or command
history. The migration service is the only service permitted to migrate the
schema. The runtime Web/API role must not become a schema owner, and the Worker
must not receive direct application-table or sequence access.

### Validate, start, and verify

The following commands are for an authorised human after the release gate is
frozen. They do not imply that a current local candidate is releasable.

```powershell
$DockerExecutable = 'docker'
$OperatorConfigRoot = '<operator-config-directory>\federated-iot'
$envFile = "$OperatorConfigRoot\.env"
$compose = '.\compose.postgres.yaml'
$project = 'federated-iot-platform-vX.X.X-mX'

# Render the effective configuration before changing runtime state.
& $DockerExecutable compose --env-file $envFile -f $compose --project-name $project config

# The migration gate completes before Web/API starts; do not add --build or pull.
& $DockerExecutable compose --env-file $envFile -f $compose --project-name $project up -d --no-build

& $DockerExecutable compose --env-file $envFile -f $compose --project-name $project ps
& $DockerExecutable compose --env-file $envFile -f $compose --project-name $project logs --tail 200
```

Verify the following before reporting a deployment result:

1. PostgreSQL and Web/API report healthy; migration completed successfully;
   the single Worker reports healthy.
2. Only Web/API has the selected host port mapping; PostgreSQL has none.
3. Web/API joins `iot_edge` and `iot_internal`; Worker and PostgreSQL are only
   on `iot_internal`.
4. Web/API uses a read-only artifact mount and Worker uses the corresponding
   writable mount.
5. Effective image IDs and configuration hashes match the approved evidence.
6. Database ACL verification proves direct Worker table access is denied and
   only the approved functions are executable.
7. API health, readiness, REST, SSE, artifact retrieval, replay, restart, and
   backup/recovery checks have completed according to their separate contracts.

For an upgrade, back up first, deploy only approved immutable image references,
and stop on migration or readiness failure. Rollback must use the tested
recovery path; it must not alter completed simulation records or repair states
directly.

## Docker Hub manual distribution

Docker Hub is an OCI registry for container images. It is not the system of
record for local release evidence. The target image must first pass the release
freeze gate and be reproducibly present locally.

The final registry reference must always include both a release tag and the
registry-returned immutable manifest digest:

```text
docker.io/<approved-namespace>/federated-iot-platform:<release-tag>@sha256:<manifest-digest>
docker.io/<approved-namespace>/federated-iot-platform-worker:<release-tag>@sha256:<manifest-digest>
```

An authorised human, in a separate terminal and after explicit distribution
approval, may perform a manual single-platform push as follows:

```powershell
# Enter registry credentials interactively; do not save them in this repository.
$DockerExecutable = 'docker'
$tag = '<approved-release-tag>'

& $DockerExecutable login docker.io
& $DockerExecutable tag "zx/federated-iot-platform:$tag" "docker.io/<approved-namespace>/federated-iot-platform:$tag"
& $DockerExecutable tag "zx/federated-iot-platform-worker:$tag" "docker.io/<approved-namespace>/federated-iot-platform-worker:$tag"
& $DockerExecutable push "docker.io/<approved-namespace>/federated-iot-platform:$tag"
& $DockerExecutable push "docker.io/<approved-namespace>/federated-iot-platform-worker:$tag"
& $DockerExecutable buildx imagetools inspect "docker.io/<approved-namespace>/federated-iot-platform:$tag"
& $DockerExecutable buildx imagetools inspect "docker.io/<approved-namespace>/federated-iot-platform-worker:$tag"
```

The human operator must record the returned manifest descriptor, per-platform
descriptors, source revision, SBOM checksum, and distribution timestamp in the
locally retained release evidence before declaring the lock frozen. A local
`linux/amd64` candidate alone is not sufficient when the approved release
scope requires `linux/arm64` as well. Never use `latest` as a release or
rollback reference.

## Zenodo manual archive distribution

Zenodo is an archival repository, not a container runtime or OCI registry. It
is suitable for a versioned offline-release package, not for serving images to
the Docker daemon at runtime.

After the release lock is frozen, generate and validate a local package using
the existing local-only exporter. The package must include image archives, a
sorted SHA-256 manifest, the release manifest, SBOMs, Compose/config templates,
and retained release evidence.

```powershell
# HUMAN-OPERATED LOCAL EXPORT. It contacts no external service.
$DockerExecutable = 'docker'
.\scripts\Export-OfflineReleasePackage.ps1 `
  -DockerExecutable $DockerExecutable `
  -OutputRoot '.\release' `
  -ReleaseId '<approved-release-id>' `
  -ReleaseManifestPath '.\offline\package\release-manifest.json' `
  -PostgresImageReference '<immutable-postgres-reference>' `
  -WebApiImageReference '<immutable-web-reference>' `
  -WorkerImageReference '<immutable-worker-reference>' `
  -SbomPath @('.\sbom\postgres.cdx.json', '.\sbom\web-api.cdx.json', '.\sbom\algorithm-worker.cdx.json') `
  -ProvenancePath @('.\release-evidence\postgres-18.6-bookworm.oci-index.json', '.\release-evidence\base-images.oci-indexes.json')
```

Before any external upload, validate the package from a disconnected host and
retain the output locally. An authorised human may then upload the validated
package through the Zenodo web interface, without placing a Zenodo token in
the repository, a script, a shell history, or a chat transcript. Upload and
publication are separate human decisions. Retain the Zenodo record URL/DOI and
the package checksum manifest locally as audit evidence.

A recipient restores an offline package only after checksum validation:

```powershell
# Run on an isolated receiving host.
$DockerExecutable = 'docker'
& $DockerExecutable load -i '.\images\<approved-image-archive>.tar'
```

The receiving host must still use exact tag-and-digest references in its
operator environment file.

## Render assessment

### Direct deployment result

The current full Docker Compose topology is **not directly portable to Render
as a configuration-only change**. Render may be useful for a future Web/API or
Worker deployment, but it cannot presently replace the local Compose runtime
without approved architecture work.

The blocking contracts are:

1. The Worker atomically writes committed artifacts to the `artifacts` volume
   while Web/API reads the same content read-only. This is a shared filesystem
   contract, not merely persistent storage.
2. The current service configuration depends on read-only bind-mounted
   `platform.yaml` and `parameter-constraints.v1.json`, plus Docker secret
   files for database credentials.
3. Web/API listens on all container interfaces at port 8080, but a public PaaS
   still requires verified mapping to its assigned port and public reverse-proxy
   contract.
4. The migration service, PostgreSQL role ownership, and minimum ACL model
   must be preserved. Runtime Web/API must still not migrate the schema.

Changing only environment variables cannot satisfy these requirements. Do not
expose PostgreSQL publicly, grant Worker table access, replace secret files
with repository values, or make Web/API writable to work around the gaps.

### Required approved adaptation before considering Render

An architecture/requirements gate must approve all of the following before a
Render implementation begins:

| Area | Required adaptation |
| --- | --- |
| Artifacts and datasets | Replace the shared Docker-volume contract with an approved shared storage abstraction, preserving atomic publish and immutable committed artifacts. |
| Database | Provision a PostgreSQL service compatible with the approved server version and ACL bootstrap; run migration as a controlled one-shot job. |
| Web listener | Verify the existing `0.0.0.0:8080` listener against the PaaS-assigned port and ingress contract. |
| Configuration and secrets | Deliver the versioned parameter constraints, platform configuration, and four separate credentials through approved managed configuration/secret mechanisms. |
| Service separation | Run Web/API as the public service and the single Worker as a non-public background service; preserve one Worker only. |
| Evidence | Re-run health, ACL, artifact integrity, REST/SSE, backup/recovery, release-lock, and QA evidence on the target platform. |

Only after those changes are approved could an operator map the logical
configuration to a hosted environment. The intended mapping would be:

| Hosted role | Required properties |
| --- | --- |
| Web/API service | Public HTTPS endpoint; non-root container; configured platform port; immutable image digest; no schema migration authority. |
| Worker service | Private/background service; exactly one instance; no public port; Worker-only database function permissions; access to approved shared storage. |
| Migration job | One-shot controlled job using `platform_migrator`; successful completion gates Web/API rollout. |
| PostgreSQL | Private connectivity only; backups; the current separate DB roles and explicit grants. |

The final Render configuration must be verified against Render's current
official documentation and the approved project architecture at the time of
deployment. This runbook does not authorise a hosted deployment or claim that
the current M1 service can run there unchanged.

## Operational decision summary

| Target | Can host the current full service now? | Approved use in this runbook |
| --- | --- | --- |
| Local Docker Engine / Docker Desktop | Yes, after the release gate and local prerequisites pass | Controlled full-stack deployment |
| Docker Hub | No runtime hosting; registry only | Human-only OCI image distribution after release freeze |
| Zenodo | No runtime hosting; archive only | Human-only validated offline package archive |
| Render | No, not without approved architecture adaptation | Future conditional target only |
