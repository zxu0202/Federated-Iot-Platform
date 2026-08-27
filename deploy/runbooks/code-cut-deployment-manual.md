# Code-Cut Deployment and Offline Package Manual

## Status and authority

This is an English operator manual for a future `1.0.0-m1` code cut. It is not
release approval and it does not change the current `release-freeze-required`
or `open` gates. Do not build, save, load, start, push, upload, publish, or
restore anything from this manual until a human authorises that exact action.

The project keeps source, images, SBOMs, packages, and evidence local. Docker
Hub publication and Zenodo publication are always separate, human-only actions.
This repository is distributed under the MIT License declared in the root
`LICENSE` file. Public source bundles must include that file and must not
substitute a different license without explicit owner approval.

## 1. Release prerequisites

Proceed only when all of the following are true:

1. The release lock is `frozen` and contains verified immutable image
   references for PostgreSQL, Web/API, and Algorithm Worker.
2. The source revision, Compose hash, migration identities, image SBOMs, and
   target platform descriptors match the approved release record.
3. Full M1 integration and independent QA passed against the same image IDs.
4. The target host profile, backup destination, and fresh-volume restore drill
   have been approved.
5. The operator has separate local files for four passwords and never places a
   password, token, or private key in source control, an image, a command log,
   or an environment variable.

The current M1 topology has one Web/API service, one migration gate, one
Algorithm Worker, and PostgreSQL. Do not add separate containers for Agent 1,
Agent 2, or Agent 3, and do not scale the Worker beyond one replica.

## 2. Locked local image builds

Run these commands only in an approved source checkout after loading all
frozen base images locally. They deliberately disable network access, pulls,
and build cache reuse. The build context is the `code` directory.

```powershell
# HUMAN-OPERATED LOCAL BUILD ONLY.
$DockerExecutable = 'docker'
$revision = '<approved-source-revision>'

& $DockerExecutable buildx build --load --progress=plain `
  --platform linux/amd64 --network=none --pull=false --no-cache `
  --file deploy/Dockerfile.web-api `
  --tag zx/federated-iot-platform:1.0.0-m1 `
  --build-arg "GO_BUILDER_IMAGE=<frozen-go-reference>" `
  --build-arg "NODE_BUILDER_IMAGE=<frozen-node-reference>" `
  --build-arg "WEB_RUNTIME_IMAGE=<frozen-debian-reference>" `
  --build-arg "BUILD_VERSION=1.0.0-m1" `
  --build-arg "BACKEND_VERSION=1.0.0-m1" `
  --build-arg "FRONTEND_VERSION=1.0.0-m1" `
  --build-arg "VCS_REF=$revision" .

& $DockerExecutable buildx build --load --progress=plain `
  --platform linux/amd64 --network=none --pull=false --no-cache `
  --file deploy/Dockerfile.algorithm-worker `
  --tag zx/federated-iot-platform-worker:1.0.0-m1 `
  --build-arg "PYTHON_BUILDER_IMAGE=<frozen-python-builder-reference>" `
  --build-arg "PYTHON_RUNTIME_IMAGE=<frozen-python-runtime-reference>" `
  --build-arg "BUILD_VERSION=1.0.0-m1" `
  --build-arg "ALGORITHM_VERSION=1.0.0-m1" `
  --build-arg "WORKER_VERSION=1.0.0-m1" `
  --build-arg "PYTHON_PACKAGE_VERSION=1.0.0.dev1" `
  --build-arg "VCS_REF=$revision" .
```

Inspect the completed local images and record their IDs, users, labels, sizes,
and platforms in local release evidence. A local image ID is not a registry
manifest digest and must never be presented as one.

```powershell
& $DockerExecutable image inspect zx/federated-iot-platform:1.0.0-m1 --format '{{json .}}'
& $DockerExecutable image inspect zx/federated-iot-platform-worker:1.0.0-m1 --format '{{json .}}'
```

## 3. Local package preparation

The frozen exporter produces two complete local packages:
`release/dockerhub/<release-id>/` and `release/zenodo/<release-id>/`. Each
contains PostgreSQL, Web/API, and Worker image archives, non-secret runtime
assets, SBOMs, safe provenance, inventory, checksums, and a package-local
validator. It deliberately omits source secrets, test data, QA reports, local
machine evidence, logs, PostgreSQL volumes, datasets, and task artifacts.

```powershell
# HUMAN-OPERATED LOCAL PACKAGE EXPORT ONLY. It performs no upload or push.
.\scripts\Export-OfflineReleasePackage.ps1 `
  -DockerExecutable $DockerExecutable `
  -OutputRoot '.\release' `
  -ReleaseId 'v1.0.0-m1' `
  -ReleaseManifestPath '.\offline\package\release-manifest.json' `
  -PostgresImageReference 'docker.io/library/postgres:<tag>@sha256:<digest>' `
  -WebApiImageReference 'docker.io/<namespace>/federated-iot-platform:<tag>@sha256:<digest>' `
  -WorkerImageReference 'docker.io/<namespace>/federated-iot-platform-worker:<tag>@sha256:<digest>' `
  -SbomPath @('.\sbom\postgres.cdx.json', '.\sbom\web-api.cdx.json', '.\sbom\algorithm-worker.cdx.json') `
  -ProvenancePath @('.\release-evidence\postgres-oci-index.json', '.\release-evidence\base-images-oci-indexes.json')
```

Validate both generated directories before transfer:

```powershell
.\release\dockerhub\v1.0.0-m1\validate\Validate-OfflinePackage.ps1 `
  -PackageDirectory '.\release\dockerhub\v1.0.0-m1'
.\release\zenodo\v1.0.0-m1\validate\Validate-OfflinePackage.ps1 `
  -PackageDirectory '.\release\zenodo\v1.0.0-m1'
```

## 4. Offline receiving-host installation

Copy one complete package through an approved channel. Verify it before loading
an archive. The validation command does not contact a registry or inspect the
source checkout.

```powershell
$PackageRoot = '<release-directory>\v1.0.0-m1'
& "$PackageRoot\validate\Validate-OfflinePackage.ps1" -PackageDirectory $PackageRoot

$DockerExecutable = 'docker'
& $DockerExecutable load --input "$PackageRoot\images\postgres.tar"
& $DockerExecutable load --input "$PackageRoot\images\web-api.tar"
& $DockerExecutable load --input "$PackageRoot\images\algorithm-worker.tar"
```

After loading, inspect the loaded tags and image IDs against
`manifests/*.image-inspect.json` and the release manifest. Stop when the local
engine cannot resolve a required immutable `name:tag@sha256:digest` reference
without a registry. This is a portability gate, not permission to replace a
frozen digest with a floating tag.

## 5. Operator configuration and secrets

Create an operator-owned configuration directory outside the received package.
Copy the templates, then use absolute paths in the operator `.env` file so
Compose can mount them reliably.

```powershell
$PackageRoot = '<release-directory>\v1.0.0-m1'
$OperatorConfigRoot = '<operator-config-directory>\federated-iot'
New-Item -ItemType Directory -Force -Path $OperatorConfigRoot, "$OperatorConfigRoot\secrets" | Out-Null
Copy-Item "$PackageRoot\runtime\.env.example" "$OperatorConfigRoot\.env"
Copy-Item "$PackageRoot\runtime\config\platform.example.yaml" "$OperatorConfigRoot\platform.yaml"
Copy-Item "$PackageRoot\runtime\config\parameter-constraints.v1.json" "$OperatorConfigRoot\parameter-constraints.v1.json"
```

Create four distinct one-line secret files with restricted filesystem access:

```powershell
& "$PackageRoot\runtime\scripts\Initialize-LocalSecrets.ps1" -OutputDirectory "$OperatorConfigRoot\secrets"
```

Update only the operator copy of `.env` with exact frozen image references,
absolute paths to `platform.yaml`, `parameter-constraints.v1.json`, and the
four distinct secret files. Never copy an active `.env`, populated
`platform.yaml`, or secret directory into the package.

The default Web/API publishing contract is:

```dotenv
PLATFORM_BIND_ADDRESS=0.0.0.0
HOST_API_PORT=8080
```

Web/API listens inside the container at `0.0.0.0:8080`; the host mapping is
`0.0.0.0:${HOST_API_PORT}:8080`. Set a host-assigned IPv4 address only when an
operator intentionally restricts ingress. PostgreSQL and Worker must never
publish host ports.

## 6. Start, health, stop, backup, and restore

Before creating containers, render the Compose configuration from the received
package and the operator-owned environment file. Use `--no-build`; packages
must not rebuild or pull.

```powershell
$compose = "$PackageRoot\runtime\compose.postgres.yaml"
$project = 'federated-iot-platform-m1'
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" -f $compose --project-name $project config
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" -f $compose --project-name $project up -d --no-build
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" -f $compose --project-name $project ps
Invoke-WebRequest "http://127.0.0.1:8080/api/v1/health/live" -UseBasicParsing
Invoke-WebRequest "http://127.0.0.1:8080/api/v1/health/ready" -UseBasicParsing
```

Verify the migration completed, PostgreSQL/Web/API/Worker are healthy, only
Web/API has a host port, and the image IDs/configuration hashes match the final
manifest. Then run the approved ACL, REST, SSE, artifact, replay, restart, and
fresh-volume backup/restore acceptance procedures.

Stop without removing volumes unless the approved recovery procedure requires
fresh isolated volumes:

```powershell
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" -f $compose --project-name $project stop
```

Backup and restore use the native PostgreSQL procedure in
`runtime/runbooks/backup-recovery.md`. Export a logical database backup and
the datasets/artifacts hash manifest before an upgrade. Restore only to fresh,
isolated volumes, then validate the recorded counts, hashes, artifact access,
and replay behavior. Never copy a live PostgreSQL volume as a backup or repair
completed task state directly.

Before a fresh-volume restore drill, run the package-local restore preflight:

```powershell
& "$PackageRoot\validate\Test-OfflineRestorePreflight.ps1" -PackageDirectory $PackageRoot
```

## 7. Troubleshooting

| Symptom | Required action |
| --- | --- |
| Package checksum fails | Stop. Obtain a complete validated package; do not pull a replacement image. |
| Loaded image cannot satisfy an immutable Compose reference | Stop at the offline digest-resolution portability gate and record the engine behavior. |
| Migration fails | Stop rollout. Preserve logs locally and use the approved backup/recovery decision. |
| Web/API is not ready | Check migration, PostgreSQL health, read-only config mounts, and exact image identity. Do not weaken ACLs. |
| Worker is unhealthy | Keep one Worker only; check repository function permissions, Worker credentials, datasets/artifacts mounts, and numeric thread limits. |
| Artifact endpoint integrity error | Verify the `runs/` relative-path contract, Web/API namespace root `/var/lib/iot`, and read-only artifacts mount. |
| Restore validation fails | Keep the original volumes intact, retain evidence locally, and repeat only in a new approved recovery batch. |

## 8. Later human-only distribution

After explicit distribution approval, a human may manually tag and push the two
application images to Docker Hub. These commands are examples only; they are
never run by project automation:

```powershell
# HUMAN ONLY. Do not save credentials in this repository.
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

Record returned registry manifest digests and platform descriptors in local
evidence. Do not use `latest` as a release reference.

Zenodo is an archive target, not a runtime registry. A human may manually
upload the already validated `release/zenodo/<release-id>/` directory through
the Zenodo web interface after separate upload and publication approvals. If a
human uses the Zenodo API instead, they must supply a token only from an
approved secret store in their own terminal; no token, command history, API
response, or DOI belongs in this repository. Upload and publication remain two
separate human decisions.

## 9. Outstanding code-cut blockers

The following block a final package and source publication today:

1. The release lock remains open and application SBOMs, immutable application
   registry references, and complete platform evidence are unfinished.
2. A clean disconnected host must demonstrate how Docker resolves each frozen
   digest reference after `docker image load`; no fallback to mutable tags is
   permitted.
3. The package-local validator is source-independent, but a full clean-host
   Compose validation and fresh-volume restore drill remain required.
4. Independent M1 QA and explicit human approval are required before package
   generation, Docker Hub distribution, Zenodo distribution, a Git tag, or a
   public source release.
