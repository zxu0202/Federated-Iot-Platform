# Local offline package operator manual

## Scope and custody

This package is a local `1.0.0-m1` runtime bundle. It contains exactly one
PostgreSQL service, one Web/API service, and one Algorithm Worker. It does not
contain source code, active secrets, datasets, result artifacts, database
volumes, or external publishing credentials.

The package is self-contained for offline validation and local Docker Engine
operation. It does not authorize a registry push, archive upload, public
release, Internet exposure, Worker scaling, or a topology change.

## 1. Verify before loading images

Copy one complete package directory through an approved channel. Verify it
before loading an image archive:

```powershell
$PackageRoot = '<release-directory>\v1.0.0-m1'
& "$PackageRoot\validate\Validate-OfflinePackage.ps1" -PackageDirectory $PackageRoot
& "$PackageRoot\validate\Test-OfflinePackageProtection.ps1" -PackageDirectory $PackageRoot
& "$PackageRoot\validate\Test-OfflineRestorePreflight.ps1" -PackageDirectory $PackageRoot
```

Stop when a checksum, manifest identity, SBOM, or protection check fails. Do
not pull a replacement image from a registry.

## 2. Create operator-owned configuration

Keep the active environment file and all secrets outside the package.

```powershell
$PackageRoot = '<release-directory>\v1.0.0-m1'
$OperatorConfigRoot = '<operator-config-directory>\federated-iot'
New-Item -ItemType Directory -Force -Path $OperatorConfigRoot, "$OperatorConfigRoot\secrets" | Out-Null
Copy-Item "$PackageRoot\runtime\.env.example" "$OperatorConfigRoot\.env"
Copy-Item "$PackageRoot\runtime\config\platform.example.yaml" "$OperatorConfigRoot\platform.yaml"
Copy-Item "$PackageRoot\runtime\config\parameter-constraints.v1.json" "$OperatorConfigRoot\parameter-constraints.v1.json"
& "$PackageRoot\runtime\scripts\Initialize-LocalSecrets.ps1" -OutputDirectory "$OperatorConfigRoot\secrets"
```

Set the following operator environment values to absolute paths. Preserve the
three exact image references already present in the template.

```dotenv
PLATFORM_CONFIG_PATH=<operator-config-directory>/federated-iot/platform.yaml
PARAMETER_CONSTRAINTS_SOURCE=<operator-config-directory>/federated-iot/parameter-constraints.v1.json
POSTGRES_ADMIN_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/postgres_admin_password.txt
WEB_API_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/web_api_db_password.txt
MIGRATOR_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/platform_migrator_db_password.txt
WORKER_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/algorithm_worker_db_password.txt
PLATFORM_BIND_ADDRESS=0.0.0.0
HOST_API_PORT=8080
```

All four secret-file paths must be distinct. Never put a password, key, token,
or certificate in this package, the environment template, command history, or
source control.

## 3. Load and start without a network pull

```powershell
$DockerExecutable = 'docker'
Get-ChildItem -LiteralPath "$PackageRoot\images" -Filter '*.tar' -File | ForEach-Object {
  & $DockerExecutable load --input $_.FullName
}

& "$PackageRoot\runtime\scripts\Start-OfflinePackage.ps1" `
  -PackageDirectory $PackageRoot `
  -EnvironmentFile "$OperatorConfigRoot\.env" `
  -ProjectName 'federated-iot-platform-m1' `
  -DockerExecutable $DockerExecutable
```

The startup script validates the package and the operator configuration, then
renders Compose and starts it with `--no-build --pull never`. PostgreSQL and
the Worker have no published host port. Web/API listens in the container on
`0.0.0.0:8080`; the host bind is controlled only by the operator environment.

Confirm that migration exited successfully and PostgreSQL, Web/API, and the
single Worker are healthy before any functional acceptance work.

## 4. Stop, backup, and restore

```powershell
& "$PackageRoot\runtime\scripts\Stop-OfflinePackage.ps1" `
  -PackageDirectory $PackageRoot `
  -EnvironmentFile "$OperatorConfigRoot\.env" `
  -ProjectName 'federated-iot-platform-m1' `
  -DockerExecutable $DockerExecutable
```

Stopping preserves volumes. Follow `runtime/runbooks/backup-recovery.md` for
backup and restore. A restore drill always uses a new isolated Compose project
and fresh volumes; never delete or overwrite an existing production volume as
part of a drill.
