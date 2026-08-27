# Connected Source Build and Local Run

## Scope

This path lets a user clone the public repository, download dependency content
through approved public registries, build local `linux/amd64` images, and run
the M1 topology. It does not reproduce or replace an official release image.
Official code cuts continue to use the offline Dockerfiles, frozen vendor tree,
npm cache, wheelhouse, SBOMs, and release evidence.

The connected Dockerfiles consume only tracked source and lock/checksum files:

- Backend uses `go.mod`, `go.sum`, `go mod verify`, and `-mod=readonly`.
- Frontend uses `package-lock.json` and `npm ci --ignore-scripts`.
- Worker uses `requirements.lock`, `--require-hashes`, and binary wheels from
  the configured Python package index.

Do not put registry credentials in a build argument, URL, environment template,
Dockerfile, or command history.

## Prerequisites

- A clean committed Git checkout. The image revision label must identify the
  exact source tree; the build script refuses dirty or untracked source.
- Docker Desktop or Docker Engine with Buildx and Linux container support.
- Network access to the configured Go proxy/checksum database, npm registry,
  Python package index, and Docker registry.
- At least 4 CPUs and 3 GiB available to Docker for the runtime baseline.

The template pins all six base images by exact tag and SHA-256 digest. Change a
registry endpoint only to an approved credential-free mirror; do not change a
dependency version or base-image digest inside this workflow.

## Prepare local runtime configuration

From `deploy/` in PowerShell:

```powershell
Copy-Item config\platform.example.yaml config\platform.yaml
.\scripts\Initialize-LocalSecrets.ps1
```

Both outputs are ignored by Git. Keep the four generated password files local
and distinct. Review `config/platform.yaml` before publishing Web/API beyond a
controlled host network.

## Build connected images

Return to the repository root and run:

```powershell
.\deploy\scripts\Build-ConnectedSourceImages.ps1
```

The script performs exactly one Web/API build and one Worker build. It:

1. requires a clean full Git revision;
2. downloads dependencies using their checked-in checksum/lock identities;
3. builds dedicated `1.0.0-m1-connected-<12-char-source-sha>` local tags;
4. reads each OCI manifest digest from Buildx `--metadata-file` output;
5. verifies platform, runtime user, version, revision, and build-profile labels;
6. pulls the digest-pinned PostgreSQL image for `linux/amd64`; and
7. writes `deploy/.env.connected.runtime` with the real Worker manifest digest.

The generated environment file is ignored by Git and contains secret-file
paths, not secret values. The script refuses to overwrite it. Review or remove
the old file deliberately before rebuilding a new source revision.

## Validate and start

```powershell
.\deploy\scripts\Test-DeploymentConfig.ps1 `
  -ConnectedSourceBuild `
  -EnvironmentFile .\deploy\.env.connected.runtime `
  -ConfigFile .\deploy\config\platform.yaml

.\deploy\scripts\Start-Platform.ps1 `
  -ConnectedSourceBuild `
  -EnvironmentFile .\deploy\.env.connected.runtime `
  -ConfigFile .\deploy\config\platform.yaml
```

Connected startup always uses `--no-build --pull never`. It starts only the
images and PostgreSQL digest already verified locally. It never weakens the
normal release-freeze gate: omitting `-ConnectedSourceBuild` still requires
frozen application digest references and `status: frozen`. The connected stack
uses the isolated `federated-iot-platform-connected` Compose project name, so
its containers, networks, and volumes do not collide with the release stack.

Verify:

```powershell
Invoke-WebRequest 'http://127.0.0.1:8080/api/v1/health/live' -UseBasicParsing
Invoke-WebRequest 'http://127.0.0.1:8080/api/v1/health/ready' -UseBasicParsing
```

Stop the connected stack without removing persistent volumes:

```powershell
.\deploy\scripts\Stop-Platform.ps1 `
  -ConnectedSourceBuild `
  -EnvironmentFile .\deploy\.env.connected.runtime
```

## Identity and failure rules

A connected rebuild normally has a different image digest from an official
release, even when source dependencies are equivalent. Use the connected tags
and generated digest only for this local build. Do not copy them into
`versions.release-freeze.yaml`, an official release manifest, or a published
release record.

Stop when any of these occurs:

- Go reports a `go.sum` mismatch or requests a `go.mod` update.
- npm reports a lockfile or integrity failure.
- pip cannot find a wheel matching a declared hash.
- Buildx metadata lacks `containerimage.digest`.
- image labels do not match the clean Git revision and M1 versions.
- deployment validation, migration, readiness, or Worker heartbeat fails.

Do not replace a checksum, disable `GOSUMDB`, remove `--require-hashes`, invent
an image digest, or reuse a previous runtime environment file to force a pass.
Dependency changes require a separate reviewed source and release batch.
