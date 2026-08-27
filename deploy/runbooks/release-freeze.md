# Release Freeze Image Evidence

`versions.release-freeze.yaml` is the single deployment image evidence record.
It must remain `release-freeze-required` until an authorised, connected release
environment verifies the following facts from registry metadata:

1. PostgreSQL is the latest stable release approved for the freeze date.
2. Every build and runtime base image has an exact versioned tag and a
   manifest-list SHA-256 digest.
3. The manifest list contains the required platform descriptors. The S1 formal
   runtime target is `linux/amd64`; `linux/arm64` is recorded to make the base
   image selection multi-architecture auditable, not to broaden the Windows or
   Linux x86_64 acceptance scope.
4. The built Web/API and Worker images have immutable repository digests,
   source revisions, and offline-generated SBOM SHA-256 values.

Do not substitute a tag, including `latest`, for a digest. Do not guess a
digest from a webpage, image cache, or a similarly named platform image.

## Required release evidence

For each image record retain:

- Fully qualified reference with exact tag and manifest-list digest.
- Per-platform descriptor digest and platform tuple.
- Image creation time, OCI labels, source revision, and local image ID.
- SBOM in SPDX or CycloneDX JSON produced without adding a runtime dependency.
- SHA-256 for the SBOM, Compose file, configuration template, image archive,
  installation material, and the final release manifest.

The offline package is built only after this record is frozen. It contains
loaded image archives, their SHA-256 values, the frozen Compose/config files,
SBOMs, version evidence, and the Windows/Linux runbooks. Runtime Compose uses
`pull_policy: never`; the package test must run with public network access
disabled.

## Current M1 base-image evidence

All six base-image records now have approved OCI index and platform-descriptor
evidence. The source references, index digests, and `linux/amd64` plus
`linux/arm64` descriptors are held in `../versions.release-freeze.yaml`.
PostgreSQL has its own compact evidence file; the other base images share
`../release-evidence/base-images.oci-indexes.json`. DaoCloud references are
recorded only as the approved metadata transport mirror. Every release reference
continues to use the original `docker.io` source.

| Release-lock key | Exact source tag | Selection boundary |
|---|---|---|
| `postgres` | `postgres:18.6-bookworm` | Approved latest stable PostgreSQL patch at freeze date |
| `go_builder` | `golang:1.23.0-bookworm` | Backend requires exactly Go 1.23.0 |
| `node_builder` | `node:14.18.0-bullseye` | Matches the approved Frontend offline validation toolchain |
| `web_runtime` | `debian:12.12-slim` | Debian-compatible runtime required by `groupadd` and `useradd` |
| `python_builder` | `python:3.12.12-bookworm` | Worker test/runtime target is Python 3.12 |
| `python_runtime` | `python:3.12.12-slim-bookworm` | Debian-compatible Python 3.12 runtime |

The base-image evidence does not freeze the release. Local builds, immutable
application image digests, image SBOMs, archive checksums, offline validation,
and QA closure remain required.

## Connected manifest evidence command

For an approved future re-verification, an authorised human may run the
following in a normal-user PowerShell session with a writable Docker client
configuration directory. It performs registry metadata requests; it does not
pull image layers, build, or start containers. Docker documents `buildx
imagetools inspect` as the registry image-inspection command and the JSON
manifest contains the index digest and platform descriptors.

```powershell
$DockerExecutable = 'docker'
$refs = @(
  'docker.io/library/postgres:18.6-bookworm@sha256:7d2695c3aa88e792e8b3b233e7e4adb296a20412c6c0ca361e3edaaacfada108',
  'docker.io/library/golang:1.23.0-bookworm@sha256:32096e84705b30bb39cc9c65ef2896efacc4268203b7876049847763cefc934d',
  'docker.io/library/node:14.18.0-bullseye@sha256:7816ee1e305841a4e551933e21b3d79727b8e0645a03186b75be7089f010cfbc',
  'docker.io/library/debian:12.12-slim@sha256:d5d3f9c23164ea16f31852f95bd5959aad1c5e854332fe00f7b3a20fcc9f635c',
  'docker.io/library/python:3.12.12-bookworm@sha256:c0abd0758831ad99b7a29e0c1a875da9c4abb9a2e3f21e2eeb585dbcadfb6cd0',
  'docker.io/library/python:3.12.12-slim-bookworm@sha256:593bd06efe90efa80dc4eee3948be7c0fde4134606dd40d8dd8dbcade98e669c'
)
$evidence = Join-Path $PWD 'registry-evidence'
New-Item -ItemType Directory -Force -Path $evidence | Out-Null
foreach ($ref in $refs) {
  $safeName = $ref.Replace('/', '_').Replace(':', '_')
  & $DockerExecutable buildx imagetools inspect $ref --format '{{json .Manifest}}' |
    Set-Content -Encoding utf8 -NoNewline (Join-Path $evidence "$safeName.manifest.json")
  if ($LASTEXITCODE -ne 0) { throw "Manifest inspection failed: $ref" }
}
```

For every resulting JSON document, retain the top-level index digest and the
`linux/amd64` and `linux/arm64` child manifest digests. The release reviewer
must compare the captured tag, index digest, and both platform digests before
writing the `image@sha256:...` reference to the version lock.

## Commands that download image layers

The following commands require registry access and download layers. They are
listed for local build/package preparation only, never against an unpinned tag.
They are not Docker Hub distribution commands and they do not authorise an
automation to distribute an image externally:

```powershell
$frozenRef = 'docker.io/library/postgres:18.6-bookworm@sha256:7d2695c3aa88e792e8b3b233e7e4adb296a20412c6c0ca361e3edaaacfada108'
& $DockerExecutable pull --platform linux/amd64 $frozenRef
& $DockerExecutable pull --platform linux/arm64 $frozenRef
& $DockerExecutable image save --output .\offline-images\postgres-18.6-bookworm.tar $frozenRef
Get-FileHash -Algorithm SHA256 .\offline-images\postgres-18.6-bookworm.tar
```

Repeat the same sequence for every frozen base image and, after their offline
builds and SBOM generation, for the immutable Web/API and Worker images.

## Remaining M1 offline inputs

- Registry manifest evidence, per-platform archives, and SHA-256 manifests for
  every frozen base image; the local inventory is currently empty.
- Go 1.23.0 delivery archive and its source checksum; the Backend vendor tree
  and dependency SBOM are already bound under `offline/go/`. Public Git omits
  the vendor tree, so an approved staging host must reconstruct it from the
  checked-in `go.mod` and `go.sum` and pass the frozen hash checks before the
  disconnected build.
- Frontend-owned `package-lock.json` and `.npm-cache` are now available, and
  the owner reports `npm ci --offline` plus the production build passing. The
  frozen Node 14.18.0 Bullseye image still must be present locally before the
  integrated image build can be accepted.
- Worker-owned wheelhouse checksum manifest and input verification evidence.
- Immutable Web/API and Worker image references, source revisions, and image
  SBOM SHA-256 values after the owner inputs build successfully.
- Active platform configuration, secret files, and QA closure evidence. These
  are deployment-time material and must not be committed to this repository.
