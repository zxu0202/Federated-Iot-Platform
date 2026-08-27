# Portable Offline Docker Package Operator Manual

## Release prerequisites

Use this procedure only when all of the following conditions are true:

1. The portable controlled-LAN deployment scope has been approved.
2. The package export includes every documented runtime asset.
3. The release lock is `frozen`, the package manifest is complete, and the
   target host profile has passed the clean-host acceptance suite.

An existing `deploy/release/` directory is not independently deployable solely
because its image archives load successfully. Validate the complete package
before installation.

Automated registry push, archive upload, publication, Internet exposure, and
topology changes are outside this procedure.

## 1. Intended operating model

The final package supports one independent S1 installation on one approved
Windows or Linux Docker Engine host. It runs the PostgreSQL-only stack locally
on that host and exposes only Web/API through a selected controlled-LAN IPv4
address and TCP port.

The final package does not provide:

- public Internet deployment;
- a shared database or shared volume between hosts;
- active-active or automatic failover;
- Worker scaling or separate containers for Agent 1, Agent 2, or Agent 3;
- Docker Swarm, Kubernetes, PaaS, or unvalidated Docker-runtime support.

## 2. Required final package inventory

Before accepting a package, the operator must confirm that it contains the
frozen layout below and that every listed file is covered by
`checksums/sha256sum.txt`.

```text
release/<release-id>/
  images/
    <postgres-runtime-image>.tar
    <web-api-runtime-image>.tar
    <algorithm-worker-runtime-image>.tar
  sbom/
  evidence/
  manifests/
    release-manifest.json
    versions.release-freeze.yaml
    <image-inspection-records>.json
  runtime/
    compose.postgres.yaml
    postgres/init/010-create-service-roles.sh
    config/.env.example
    config/platform.example.yaml
    config/parameter-constraints.v1.json
    scripts/
    runbooks/
  checksums/sha256sum.txt
  README.md
```

The package must not include active secrets, completed-task data, a PostgreSQL
data volume, customer datasets, user result artifacts, registry credentials,
or Zenodo credentials.

## 3. Target host prerequisites

The operator records the following values in the installation record before
starting:

| Item | Required condition |
| --- | --- |
| Host profile | Approved Windows Docker Desktop x86_64 or Linux Docker Engine x86_64; arm64 only when its separately verified package is supplied |
| Container mode | Linux containers |
| Docker capability | Docker Engine and the approved Compose v2 profile |
| Resources | At least the frozen release-manifest CPU, memory, disk, and volume budget |
| Network | A selected host IPv4 owned by the target host and an approved LAN firewall rule for the Web/API port only |
| Storage | Local named-volume capacity for PostgreSQL, datasets, artifacts, logs, and a future backup |
| Recovery | Approved backup destination and restore-drill procedure |

The operator must not install a package built for a different CPU architecture.
The package manifest and the local image inspection records must agree on the
target platform before the stack is started.

## 4. Receive and verify the package

Copy the package through the approved internal transfer channel or physical
media. The package can be transferred over a controlled network, but the
project itself performs no automatic transfer.

On Windows, verify checksums before opening an image archive:

```powershell
$PackageRoot = '<release-directory>\<approved-release-id>'

Get-Content "$PackageRoot\checksums\sha256sum.txt" | ForEach-Object {
  if ($_ -notmatch '^(?<hash>[a-f0-9]{64})  (?<path>.+)$') {
    throw 'Invalid checksum record.'
  }
  $actual = (Get-FileHash -LiteralPath (Join-Path $PackageRoot $Matches.path) -Algorithm SHA256).Hash.ToLowerInvariant()
  if ($actual -ne $Matches.hash) {
    throw "Checksum mismatch: $($Matches.path)"
  }
}
```

Stop immediately if a checksum, release-lock state, image reference, SBOM
reference, platform descriptor, or required runtime asset is missing. Do not
repair a received package by pulling a replacement image from a registry.

## 5. Prepare operator-owned configuration

Create an operator-owned configuration directory outside the received package.
Copy the approved templates, then set only host-specific values and exact
frozen image references. Do not alter an image digest, fixed S1 item, database
role name, or Worker capacity.

```powershell
$PackageRoot = '<release-directory>\<approved-release-id>'
$OperatorConfigRoot = '<operator-config-directory>\federated-iot'

New-Item -ItemType Directory -Force -Path $OperatorConfigRoot | Out-Null
Copy-Item "$PackageRoot\runtime\.env.example" "$OperatorConfigRoot\.env"
Copy-Item "$PackageRoot\runtime\config\platform.example.yaml" "$OperatorConfigRoot\platform.yaml"
Copy-Item "$PackageRoot\runtime\config\parameter-constraints.v1.json" "$OperatorConfigRoot\parameter-constraints.v1.json"
```

The active environment file must point to the operator-owned configuration and
secret paths, for example:

```dotenv
PLATFORM_CONFIG_PATH=<operator-config-directory>/federated-iot/platform.yaml
PARAMETER_CONSTRAINTS_SOURCE=<operator-config-directory>/federated-iot/parameter-constraints.v1.json
POSTGRES_ADMIN_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/postgres_admin_password.txt
MIGRATOR_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/platform_migrator_db_password.txt
WEB_API_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/web_api_db_password.txt
WORKER_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/algorithm_worker_db_password.txt
PLATFORM_BIND_ADDRESS=0.0.0.0
HOST_API_PORT=<approved-lan-port>
WORKER_INSTANCE_ID=algorithm-worker-1
```

The default mapping publishes Web/API on all host IPv4 interfaces. An operator
may replace `PLATFORM_BIND_ADDRESS` with an IPv4 address assigned to the target
host to deliberately restrict ingress. The four secret files must be distinct. After the approved package scripts are
available, an operator may create missing secret files without printing or
overwriting values:

```powershell
& "$PackageRoot\runtime\scripts\Initialize-LocalSecrets.ps1" `
  -OutputDirectory "$OperatorConfigRoot\secrets"
```

The operator must independently configure the LAN firewall so that only the
selected Web/API address/port is reachable from the approved LAN range. No
PostgreSQL or Worker host port is permitted.

## 6. Load and validate images

Use the Docker executable installed on the target host. The example below does
not contact a registry.

```powershell
$DockerExecutable = 'docker'

Get-ChildItem -LiteralPath "$PackageRoot\images" -Filter '*.tar' -File |
  ForEach-Object { & $DockerExecutable load -i $_.FullName }
```

Before starting, run the approved package-local validator introduced by the
portable-package implementation. It must verify all of the following without
requiring a source checkout:

1. The release lock is frozen and its image references are immutable.
2. PostgreSQL, Web/API, and Worker images are present with the manifest's
   target platform and expected identity.
3. The copied parameter constraints JSON matches the manifest SHA-256 and the
   69-path/67-editable/2-fixed contract.
4. All Compose-relative runtime assets exist.
5. The four secret paths are distinct and available without printing their
   contents.
6. The selected bind address belongs to the target host.

The implementation must provide this exact validator command in the final
package README. A validation failure is a no-start condition.

## 7. Start and verify the closed loop

Only after package validation succeeds may the human operator render and start
the stack. The final package README must provide the frozen command form. Its
behaviour must be equivalent to:

```powershell
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" `
  -f "$PackageRoot\runtime\compose.postgres.yaml" `
  --project-name 'federated-iot-platform-m1' config

& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" `
  -f "$PackageRoot\runtime\compose.postgres.yaml" `
  --project-name 'federated-iot-platform-m1' up -d --no-build

& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" `
  -f "$PackageRoot\runtime\compose.postgres.yaml" `
  --project-name 'federated-iot-platform-m1' ps
```

The operator records the rendered configuration hash, container image IDs,
health status, selected LAN endpoint, host/runtime versions, and package
checksum-manifest hash. The following closed-loop checks are mandatory:

| Check | Expected result |
| --- | --- |
| Startup order | PostgreSQL healthy, migration successful, then Web/API and the single Worker healthy |
| Network | Exactly one Web/API host mapping; PostgreSQL and Worker remain internal only |
| ACL | Worker direct table/sequence access denied; only approved function access remains |
| S1 operation | CSV import, parameter snapshot, one run, artifacts, history, replay, and export complete |
| Persistence | A controlled restart preserves PostgreSQL data and committed artifacts |
| Recovery | Backup/restore drill succeeds into fresh volumes according to the approved runbook |

Container health alone is not an acceptance result. Report product or release
success only after the complete release verification accepts the evidence.

## 8. Stop, upgrade, and rollback

Before an upgrade, create and verify the approved PostgreSQL/data/artifact
backup. An upgrade changes only to a separately approved package and immutable
image references. On migration, health, integrity, or ACL failure, stop the
upgrade and use the tested restore procedure; do not edit task states, grant
new Worker table privileges, or reuse an unverified image archive.

Stopping a project does not delete volumes. Volume deletion, data replacement,
and registry or Zenodo publication are outside this manual and require a
separate human decision.

## 9. Installation record

For each target host, retain locally:

- package release ID and checksum-manifest SHA-256;
- target host operating system, CPU architecture, Docker Engine, and Compose
  versions;
- exact image references and local image IDs;
- rendered Compose configuration hash and selected LAN endpoint;
- secret-file existence result only, never their contents;
- health, ACL, S1 closed-loop, restart, and recovery evidence;
- operator, approval reference, installation time, and any deviations.

This record makes the installation reproducible without converting it into an
external publication or a claim of cloud, public-network, or multi-host
support.
