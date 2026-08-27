# Go 1.23.0 Offline Supply-Chain Input

This directory defines the reproducible offline inputs for Backend verification.
It contains no Go archive, module, checksum, or digest invented by this project.
The public Git repository intentionally omits `backend/vendor/`. An approved
networked staging environment reconstructs that tree from the checked-in
`go.mod` and `go.sum`, verifies it against the frozen identity, and then
delivers it to the disconnected release environment. The offline delivery must
provide the following material before Backend integration evidence is accepted:

1. A Go 1.23.0 toolchain archive for each accepted host platform, plus a
   SHA-256 manifest created by the delivery source.
2. A Go builder image archive whose exact tag, manifest-list digest, and target
   platform digest are recorded in `../../versions.release-freeze.yaml`.
3. Staging-generated `vendor/` and `vendor/modules.txt`, based on the checked-in
   Backend `go.mod` and `go.sum`. DevOps records the matching canonical
   vendor-tree SHA-256 in `backend-vendor.tree.sha256` without modifying
   Backend-owned module files.
4. If a module cache is supplied in addition to `vendor/`, a separately hashed
   archive or directory manifest. The offline verifier then runs `go mod verify`.
5. SPDX or CycloneDX SBOM material and its SHA-256 for the Go builder and the
   final Web/API image.

The verified Go version is exactly `go1.23.0`. The required execution
environment is `GOTOOLCHAIN=local`, `GOPROXY=off`, and `GOSUMDB=off`. Neither
the verifier nor the Dockerfile may download a toolchain or a module.

## Approved staging preparation

Run this only on the approved networked staging host, before transferring the
completed dependency input to the disconnected release environment:

```powershell
Set-Location backend
$env:GOTOOLCHAIN = 'local'
go mod download
go mod verify
go mod vendor
```

Use the approved `GOPROXY` and checksum database policy. Do not force-add the
generated `vendor/` directory to public Git. After transfer, the offline
verifier must reproduce the frozen `vendor/modules.txt` and vendor-tree hashes.

## Backend input format

`backend-vendor.tree.sha256` is a canonical tree checksum rather than a copied
file list. It is generated from the exact Backend vendor snapshot by:

```text
1. Computing SHA-256 for every regular file below vendor/.
2. Writing <file-sha256><two spaces><vendor-relative-path> with a LF newline.
3. Sorting those lines bytewise and computing SHA-256 over the resulting UTF-8 bytes.
```

The verifier recomputes every file hash, covers unexpected files, and compares
the canonical aggregate. `backend-offline-inputs.sha256` separately binds the
real `go.mod`, `go.sum`, `vendor/modules.txt`, vendor-tree manifest, and
CycloneDX dependency SBOM. Both files are DevOps-owned release material and
must be regenerated when the backend dependency snapshot changes.

## Test entry

Windows:

```powershell
.\deploy\scripts\Test-BackendOfflineSupplyChain.ps1
```

Linux:

```sh
sh deploy/scripts/test-backend-offline-supply-chain.sh
```

Both commands are intended to run from `code/`. They must be executed from the
offline delivery rather than an ambient developer module cache. A failed or
unavailable toolchain fails the offline supply-chain gate and is never
converted into a passing result.
