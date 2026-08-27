# v1.0.0-m1 Image Distribution and Windows Deployment Manual

## 1. Scope

This manual covers human-operated distribution and installation of release
`1.0.0-m1`. The release remains under local custody:

- Docker Hub package: `deploy/release/dockerhub/v1.0.0-m1/`
- Zenodo package: `deploy/release/zenodo/v1.0.0-m1/`
- Target platform: `linux/amd64`
- Runtime topology: PostgreSQL, Web/API, and one Algorithm Worker
- Default host Web port: `8081`

Each package is a complete offline bundle with three image archives, Compose,
non-secret configuration templates, initialization scripts, SBOMs, manifests,
an inventory, and validators. It contains no source code, test data, run
results, database volumes, passwords, or platform access tokens.

All login, push, upload, and publication actions are performed manually in a
controlled terminal. Uploading a Zenodo draft and publishing it are separate
approval decisions. Do not add a `latest` tag, change the external version, or
republish the upstream PostgreSQL image without separate approval.

Official references:

- [Install Docker Desktop on Windows](https://docs.docker.com/desktop/setup/install/windows-install/)
- [Push images to Docker Hub](https://docs.docker.com/docker-hub/repos/manage/hub-images/push/)
- [docker login](https://docs.docker.com/reference/cli/docker/login/)
- [docker image load](https://docs.docker.com/reference/cli/docker/image/load/)
- [Zenodo REST API](https://developers.zenodo.org/)

## 2. Distribution targets

### 2.1 Local image archives

The Docker Hub and Zenodo packages contain identical image archives:

| File | Purpose | Bytes | SHA-256 |
|---|---|---:|---|
| `images/web-api.tar` | Web/API and browser application | 32,212,992 | `02a229d5a882618f7169758bfe9f21b6dc9e26d7bc8a787665631bec5a1702ee` |
| `images/algorithm-worker.tar` | Algorithm Worker | 75,288,576 | `e5ad784c23157c1938d599c1712a055c657667181913c19b6fbd3251011b2e73` |
| `images/postgres.tar` | PostgreSQL 18.6 offline dependency | 163,495,936 | `d81c65e359123918a76e5d5006a9b61c1a2a5f47758bbbc58941f88464a5a64c` |

Frozen identities:

```text
zx/federated-iot-platform:1.0.0-m1
  image ID: sha256:b39134e5d418af01e7e84db63e30d0456b5dca5fb74e696c3c4c81339588eb4b

zx/federated-iot-platform-worker:1.0.0-m1
  image ID: sha256:09e53c3ebcf2c319892bcb8ccea2230db32a69392bd00c875378533f79f15d3d

PostgreSQL 18.6 Bookworm
  digest: sha256:7d2695c3aa88e792e8b3b233e7e4adb296a20412c6c0ca361e3edaaacfada108
```

### 2.2 Docker Hub targets

Docker Hub receives images, not `tar` files. Load the two project images from:

```text
deploy/release/dockerhub/v1.0.0-m1/images/web-api.tar
deploy/release/dockerhub/v1.0.0-m1/images/algorithm-worker.tar
```

The approved remote targets are:

```text
docker.io/<approved-namespace>/federated-iot-platform:1.0.0-m1
docker.io/<approved-namespace>/federated-iot-platform-worker:1.0.0-m1
```

`postgres.tar` remains part of the complete offline package. Docker Hub
distribution keeps the frozen upstream PostgreSQL reference and does not copy
that image into the project namespace by default.

### 2.3 Zenodo upload files

Zenodo receives files rather than directories. Upload these two generated
files:

```text
deploy/release/zenodo/upload/v1.0.0-m1/
  zx-federated-iot-platform-v1.0.0-m1-linux-amd64-offline.zip
  zx-federated-iot-platform-v1.0.0-m1-linux-amd64-offline.zip.sha256
```

The ZIP contains the complete `deploy/release/zenodo/v1.0.0-m1/` layout,
including its internal `checksums/sha256sum.txt`. Do not upload only the three
image archives; the Compose file, manifests, SBOMs, configuration templates,
and validators are required for a complete installation.

## 3. Pre-distribution validation

Open PowerShell in the project `code` directory:

```powershell
$dockerHubPackage = (Resolve-Path '.\deploy\release\dockerhub\v1.0.0-m1').Path
$zenodoPackage = (Resolve-Path '.\deploy\release\zenodo\v1.0.0-m1').Path

& "$dockerHubPackage\validate\Validate-OfflinePackage.ps1" `
  -PackageDirectory $dockerHubPackage
& "$zenodoPackage\validate\Validate-OfflinePackage.ps1" `
  -PackageDirectory $zenodoPackage
```

Both commands must report `Offline package validation passed.` Then run:

```powershell
& "$dockerHubPackage\validate\Test-OfflinePackageProtection.ps1" `
  -PackageDirectory $dockerHubPackage
& "$dockerHubPackage\validate\Test-OfflineImageArchiveContent.ps1" `
  -PackageDirectory $dockerHubPackage
& "$dockerHubPackage\validate\Test-OfflineRestorePreflight.ps1" `
  -PackageDirectory $dockerHubPackage
```

Stop distribution when a validation fails. Preserve the package and error
output; do not replace an individual file.

## 4. Push to Docker Hub manually

### 4.1 Prepare repositories

1. Sign in to Docker Hub in a browser.
2. Create `federated-iot-platform` and
   `federated-iot-platform-worker` in the approved namespace.
3. Select Public or Private according to the release approval.
4. Use the `1.0.0-m1` version tag.

### 4.2 Load and inspect the images

```powershell
$DockerExecutable = 'docker'
$PackageRoot = (Resolve-Path '.\deploy\release\dockerhub\v1.0.0-m1').Path

& $DockerExecutable load --input "$PackageRoot\images\web-api.tar"
& $DockerExecutable load --input "$PackageRoot\images\algorithm-worker.tar"

& $DockerExecutable image inspect 'zx/federated-iot-platform:1.0.0-m1' `
  --format '{{.Id}} {{.Os}}/{{.Architecture}} {{.Config.User}}'
& $DockerExecutable image inspect 'zx/federated-iot-platform-worker:1.0.0-m1' `
  --format '{{.Id}} {{.Os}}/{{.Architecture}} {{.Config.User}}'
```

Expected output identities:

```text
sha256:b39134e5... linux/amd64 webapi:platform
sha256:09e53c3e... linux/amd64 worker:platform
```

### 4.3 Log in, tag, and push

```powershell
$namespace = '<approved-Docker-Hub-namespace>'
$version = '1.0.0-m1'
$remoteWeb = "docker.io/$namespace/federated-iot-platform:$version"
$remoteWorker = "docker.io/$namespace/federated-iot-platform-worker:$version"

# Enter the password or access token interactively.
& $DockerExecutable login docker.io --username $namespace

& $DockerExecutable tag "zx/federated-iot-platform:$version" $remoteWeb
& $DockerExecutable tag "zx/federated-iot-platform-worker:$version" $remoteWorker
& $DockerExecutable push $remoteWeb
& $DockerExecutable push $remoteWorker
```

Record the registry manifest digest printed by each push. A registry manifest
digest is the remote release identity; do not substitute a local image ID.

```powershell
& $DockerExecutable buildx imagetools inspect $remoteWeb
& $DockerExecutable buildx imagetools inspect $remoteWorker
& $DockerExecutable logout docker.io
```

Confirm the `1.0.0-m1` tags and `linux/amd64` platform on the Docker Hub Tags
pages.

## 5. Prepare and upload the Zenodo package

### 5.1 Create the ZIP and checksum

Run from `code`. Keep generated upload files outside the frozen package:

```powershell
$source = (Resolve-Path '.\deploy\release\zenodo\v1.0.0-m1').Path
$staging = '.\deploy\release\zenodo\upload\v1.0.0-m1'
$zipName = 'zx-federated-iot-platform-v1.0.0-m1-linux-amd64-offline.zip'
$zip = Join-Path $staging $zipName

New-Item -ItemType Directory -Force -Path $staging | Out-Null
if (Test-Path -LiteralPath $zip) {
  throw "The target ZIP already exists: $zip"
}

tar.exe -a -c -f $zip -C $source .
$zipHash = (Get-FileHash -LiteralPath $zip -Algorithm SHA256).Hash.ToLowerInvariant()
"$zipHash  $zipName" | Set-Content -LiteralPath "$zip.sha256" `
  -Encoding ascii -NoNewline
```

### 5.2 Verify the generated ZIP

```powershell
$verifyDirectory = Join-Path $staging 'verify-extracted'
if (Test-Path -LiteralPath $verifyDirectory) {
  throw "The verification directory already exists: $verifyDirectory"
}

New-Item -ItemType Directory -Path $verifyDirectory | Out-Null
tar.exe -x -f $zip -C $verifyDirectory
& "$verifyDirectory\validate\Validate-OfflinePackage.ps1" `
  -PackageDirectory $verifyDirectory
```

Proceed only after the extracted package passes validation.

### 5.3 Zenodo browser upload (recommended)

1. Sign in to Zenodo and create a new Upload draft of type Software.
2. Upload the ZIP and `.sha256` files.
3. Set the version to `1.0.0-m1` and enter approved title, creator,
   description, and keywords.
4. Select MIT to match the repository root `LICENSE`; do not substitute a
   different license without explicit release-owner approval.
5. Compare the draft file names and sizes with the local files.
6. Store the draft URL and deposition ID outside the source repository.
7. Keep the record in Draft. Publish and DOI creation require separate human
   approval.

### 5.4 Zenodo API draft upload (optional)

The following commands create and populate a draft. They do not publish it:

```powershell
$secureToken = Read-Host 'Zenodo access token' -AsSecureString
$credential = [pscredential]::new('zenodo', $secureToken)
$plainToken = $credential.GetNetworkCredential().Password
$headers = @{ Authorization = "Bearer $plainToken" }

try {
  $deposit = Invoke-RestMethod `
    -Uri 'https://zenodo.org/api/deposit/depositions' `
    -Method Post -Headers $headers -ContentType 'application/json' -Body '{}'

  $zipFile = Get-Item -LiteralPath $zip
  $sumFile = Get-Item -LiteralPath "$zip.sha256"
  Invoke-RestMethod -Uri "$($deposit.links.bucket)/$($zipFile.Name)" `
    -Method Put -Headers $headers -InFile $zipFile.FullName
  Invoke-RestMethod -Uri "$($deposit.links.bucket)/$($sumFile.Name)" `
    -Method Put -Headers $headers -InFile $sumFile.FullName

  $metadata = @{
    metadata = @{
      title       = '<approved title>'
      upload_type = 'software'
      description = '<approved description>'
      version     = '1.0.0-m1'
      creators    = @(@{ name = '<family name, given name or organization>' })
    }
  } | ConvertTo-Json -Depth 8

  $draft = Invoke-RestMethod `
    -Uri "https://zenodo.org/api/deposit/depositions/$($deposit.id)" `
    -Method Put -Headers $headers -ContentType 'application/json' -Body $metadata
  $draft | Select-Object id, title, links
}
finally {
  Remove-Variable plainToken, headers -ErrorAction SilentlyContinue
}
```

Publishing uses `POST /api/deposit/depositions/<id>/actions/publish`. Run that
operation only after separate approval of metadata, license, and checksums.

## 6. Install Docker Desktop on Windows

### 6.1 Host prerequisites

1. Use a Windows x86_64 host with WSL 2 and hardware virtualization.
2. Enable virtualization in BIOS/UEFI.
3. Configure Docker Desktop for Linux containers.
4. Allocate at least 4 CPUs and 3 GiB of Docker memory for the package
   readiness baseline.
5. Keep host port `8081` available.

Open an elevated PowerShell terminal and install or update WSL:

```powershell
wsl --install
wsl --update
wsl --version
```

Restart Windows when requested. Download Docker Desktop Installer from the
official Docker site. Use the graphical installer or run:

```powershell
$InstallerPath = '<docker-desktop-installer-path>'
Start-Process -FilePath $InstallerPath -Wait -ArgumentList @(
  'install',
  '--accept-license',
  '--backend=wsl-2'
)
```

The application installation path and the Docker WSL data path are separate.
Change the image and volume data location through supported Docker Desktop
settings; do not move an active data directory manually.

Start Docker Desktop, wait for Engine running, and select Linux containers.

### 6.2 Check Docker readiness

```powershell
$DockerExecutable = 'docker'
& $DockerExecutable version
& $DockerExecutable compose version
& $DockerExecutable info --format '{{.OSType}}/{{.Architecture}} CPUs={{.NCPU}} Memory={{.MemTotal}}'

$PackageRoot = '<release-directory>\v1.0.0-m1'
& "$PackageRoot\runtime\scripts\Test-DockerDesktopReadiness.ps1" `
  -DockerExecutable $DockerExecutable
```

Proceed when the script reports `Result : READY`.

## 7. Install and run the offline package on Windows

### 7.1 Prepare operator-owned configuration

Extract the Zenodo ZIP to `<release-directory>\v1.0.0-m1\`. Keep active
configuration and secrets outside the package:

```powershell
$PackageRoot = '<release-directory>\v1.0.0-m1'
$OperatorConfigRoot = '<operator-config-directory>\federated-iot'

& "$PackageRoot\validate\Validate-OfflinePackage.ps1" `
  -PackageDirectory $PackageRoot

New-Item -ItemType Directory -Force -Path $OperatorConfigRoot | Out-Null
Copy-Item "$PackageRoot\runtime\.env.example" "$OperatorConfigRoot\.env"
Copy-Item "$PackageRoot\runtime\config\platform.example.yaml" `
  "$OperatorConfigRoot\platform.yaml"
Copy-Item "$PackageRoot\runtime\config\parameter-constraints.v1.json" `
  "$OperatorConfigRoot\parameter-constraints.v1.json"
& "$PackageRoot\runtime\scripts\Initialize-LocalSecrets.ps1" `
  -OutputDirectory "$OperatorConfigRoot\secrets"
```

Edit `$OperatorConfigRoot\.env`:

```dotenv
PLATFORM_CONFIG_PATH=<operator-config-directory>/federated-iot/platform.yaml
PARAMETER_CONSTRAINTS_SOURCE=<operator-config-directory>/federated-iot/parameter-constraints.v1.json
POSTGRES_ADMIN_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/postgres_admin_password.txt
WEB_API_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/web_api_db_password.txt
MIGRATOR_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/platform_migrator_db_password.txt
WORKER_DB_PASSWORD_SOURCE=<operator-config-directory>/federated-iot/secrets/algorithm_worker_db_password.txt
PLATFORM_BIND_ADDRESS=0.0.0.0
HOST_API_PORT=8081
```

Preserve the three frozen image references and all version values. Set `8081`
only in the operator-owned `.env`; do not edit the package `.env.example`.

### 7.2 Load the images

```powershell
& $DockerExecutable load --input "$PackageRoot\images\postgres.tar"
& $DockerExecutable load --input "$PackageRoot\images\web-api.tar"
& $DockerExecutable load --input "$PackageRoot\images\algorithm-worker.tar"
```

### 7.3 Start the stack

```powershell
$project = 'federated-iot-platform-m1'
& "$PackageRoot\runtime\scripts\Start-OfflinePackage.ps1" `
  -PackageDirectory $PackageRoot `
  -EnvironmentFile "$OperatorConfigRoot\.env" `
  -ProjectName $project `
  -DockerExecutable $DockerExecutable
```

The script validates the package, operator configuration, and Compose before
starting with `--no-build --pull never`. PostgreSQL and the Worker publish no
host ports. Web/API listens on `0.0.0.0:8080` in the container and is published
as `0.0.0.0:8081` on the host.

### 7.4 Verify operation

```powershell
$compose = "$PackageRoot\runtime\compose.postgres.yaml"
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" -f $compose `
  --project-name $project ps --all
& $DockerExecutable compose --env-file "$OperatorConfigRoot\.env" -f $compose `
  --project-name $project logs migration

Invoke-WebRequest 'http://127.0.0.1:8081/api/v1/health/live' -UseBasicParsing
Invoke-WebRequest 'http://127.0.0.1:8081/api/v1/health/ready' -UseBasicParsing
```

Proceed when migration exits with code 0; PostgreSQL, Web/API, and Algorithm
Worker are healthy; readiness reports database `ok` and an observed Worker;
and `http://127.0.0.1:8081/` opens the English default page.

### 7.5 Controlled LAN access

```powershell
Get-NetIPAddress -AddressFamily IPv4 |
  Where-Object {
    $_.AddressState -eq 'Preferred' -and
    $_.IPAddress -notlike '127.*' -and
    $_.IPAddress -notlike '169.254.*'
  } |
  Select-Object InterfaceAlias, IPAddress
```

Other LAN clients use `http://<host-IPv4>:8081/`. When required, an
administrator may permit only the private local subnet:

```powershell
New-NetFirewallRule `
  -DisplayName 'ZX Federated IoT Platform 8081' `
  -Direction Inbound -Action Allow -Protocol TCP -LocalPort 8081 `
  -Profile Private -RemoteAddress LocalSubnet
```

Do not publish PostgreSQL port 5432. Database traffic remains on the internal
Compose network.

### 7.6 Stop and restart

```powershell
& "$PackageRoot\runtime\scripts\Stop-OfflinePackage.ps1" `
  -PackageDirectory $PackageRoot `
  -EnvironmentFile "$OperatorConfigRoot\.env" `
  -ProjectName $project `
  -DockerExecutable $DockerExecutable
```

This preserves the database, dataset, and result volumes. Repeat section 7.3
to restart. Do not run `docker compose down -v`; it removes persistent volumes.
Use `runtime/runbooks/backup-recovery.md` for backup and restore.

## 8. Human release records

Keep the following records outside the source repository:

- the approved `1.0.0-m1` source commit and local tag;
- both Docker Hub repository tags and registry manifest digests;
- the Zenodo draft ID and final record/DOI when publication is approved;
- ZIP and `.sha256` names, sizes, and hashes;
- the release manifest, inventory, SBOM hashes, and package checksum manifest;
- operator, date, approval, and deviation records.

Do not place passwords, access tokens, private keys, credential stores, raw API
responses, datasets, run results, or database volumes in source control or a
release package.
