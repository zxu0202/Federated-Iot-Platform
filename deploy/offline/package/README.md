# Local Offline Release Package Format

This directory contains tracked package templates and validators only. It does
not contain a release package, image archive, secret, or published artifact.
Generated output belongs under `deploy/release/`, which is ignored by Git and
remains under local operator custody.

The package exporter is intentionally local-only. It uses `docker image
inspect` and `docker image save` only for images already present on the
operator workstation. It has no code path for image pulling, registry pushing,
Zenodo upload, release publishing, or credential transmission.

## Frozen-package preconditions

An operator may run the exporter only after a human approves the action and all
of the following are true:

1. `versions.release-freeze.yaml` is `frozen` with exact immutable image
   references and no placeholder values.
2. The approved PostgreSQL, Web/API, and Algorithm Worker images are present
   locally for the target platform.
3. A final release manifest, three image SBOMs, safe provenance records, and
   independent M1 QA evidence are available locally.
4. The clean-host portability blockers in
   `runbooks/code-cut-deployment-manual.md` are closed by an approved test.

## Independent output directories

The future exporter creates two complete, independent local directories. Both
are self-contained so that a human can validate either one without a source
checkout:

```text
release/
  dockerhub/
    v1.0.0-m1/
      images/
        postgres.tar
        web-api.tar
        algorithm-worker.tar
      sbom/
      provenance/
      manifests/
      runtime/
      validate/
      checksums/
      README.md
  zenodo/
    v1.0.0-m1/
      <the same complete package layout>
```

`dockerhub/` is a local registry-distribution bundle and `zenodo/` is a local
archive-distribution bundle. Neither directory is an authorisation to push or
upload. The same complete runtime contents are intentionally retained in each
directory so recipients can validate and install from either independently.

## Required package inventory

Each package includes only the following classes of material:

- `images/`: Docker save archives for PostgreSQL, Web/API, and Algorithm
  Worker exact references.
- `sbom/`: one frozen SBOM for each runtime image.
- `provenance/`: safe immutable descriptor records only; no local smoke, QA,
  machine, host, user, secret, or credential evidence.
- `manifests/`: final release manifest, frozen lock, local image inspections,
  and package inventory.
- `runtime/`: Compose, `.env.example`, non-secret configuration templates,
  PostgreSQL init script, selected non-secret scripts, and runbooks.
- `validate/`: PowerShell and POSIX package/restore-preflight validators that
  run without a source checkout.
- `checksums/sha256sum.txt`: sorted SHA-256 records for all package files.

Packages must never contain populated `.env` files, `platform.yaml`, secret
files, source repositories, test datasets, customer data, PostgreSQL volumes,
task artifacts, logs, QA reports, registry credentials, or Zenodo credentials.

## Local validation

Validate a received package before loading an image or creating a volume:

```powershell
$PackageRoot = '<release-directory>\v1.0.0-m1'
& "$PackageRoot\validate\Validate-OfflinePackage.ps1" -PackageDirectory $PackageRoot
```

```sh
sh validate/validate-offline-package.sh /opt/release/zenodo/v1.0.0-m1
```

The validator checks package shape, frozen-lock state, manifest structure, and
every listed SHA-256 checksum. Loading images, creating secrets, starting
Compose, and running a fresh-volume restore drill remain separate human
operator actions described in `runbooks/code-cut-deployment-manual.md`.
