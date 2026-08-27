[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$OutputRoot,
    [Parameter(Mandatory)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')]
    [string]$ReleaseId,
    [Parameter(Mandatory)]
    [string]$ReleaseManifestPath,
    [Parameter(Mandatory)]
    [string]$PostgresImageReference,
    [Parameter(Mandatory)]
    [string]$WebApiImageReference,
    [Parameter(Mandatory)]
    [string]$WorkerImageReference,
    [Parameter(Mandatory)]
    [ValidateNotNullOrEmpty()]
    [string[]]$SbomPath,
    [string[]]$ProvenancePath = @(),
    [string]$DockerExecutable = "docker",
    [string]$ReleaseLockPath = (Join-Path $PSScriptRoot "..\versions.release-freeze.yaml"),
    [ValidateSet("both", "dockerhub", "zenodo")]
    [string]$TargetDistribution = "both"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$deployRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot ".." )).Path

function Resolve-InputFile([string]$Path, [string]$Label) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label is missing: $Path"
    }
    return (Resolve-Path -LiteralPath $Path).Path
}

function Resolve-DockerExecutable([string]$RequestedExecutable) {
    if ([IO.Path]::IsPathRooted($RequestedExecutable)) {
        if (-not (Test-Path -LiteralPath $RequestedExecutable -PathType Leaf)) {
            throw "The requested Docker executable does not exist: $RequestedExecutable"
        }
        return (Resolve-Path -LiteralPath $RequestedExecutable).Path
    }
    $command = Get-Command $RequestedExecutable -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Docker CLI is not visible in this process PATH. Supply -DockerExecutable with an absolute path."
    }
    return $command.Source
}

function Get-RelativePath([string]$Root, [string]$Path) {
    return $Path.Substring($Root.Length).TrimStart('\', '/').Replace('\', '/')
}

function Copy-UniqueFiles([string[]]$Inputs, [string]$Destination, [string]$Label) {
    $names = [System.Collections.Generic.HashSet[string]]::new([StringComparer]::OrdinalIgnoreCase)
    foreach ($inputPath in $Inputs) {
        $resolved = Resolve-InputFile $inputPath $Label
        $name = [IO.Path]::GetFileName($resolved)
        if (-not $names.Add($name)) {
            throw "$Label inputs have duplicate leaf names: $name"
        }
        Copy-Item -LiteralPath $resolved -Destination (Join-Path $Destination $name)
    }
}

function Copy-RuntimeFile([string]$RelativePath, [string]$RuntimeRoot) {
    $source = Resolve-InputFile (Join-Path $deployRoot $RelativePath) "Runtime asset"
    $destination = Join-Path $RuntimeRoot $RelativePath
    $destinationDirectory = Split-Path -Parent $destination
    New-Item -ItemType Directory -Path $destinationDirectory -Force | Out-Null
    Copy-Item -LiteralPath $source -Destination $destination
}

function Assert-SafeProvenanceInput([string]$Path) {
    $name = [IO.Path]::GetFileName($Path)
    if ($name -match '(?i)(local|qa|smoke|deployment|machine|host|secret|credential|token|password)') {
        throw "Provenance input is not safe for a portable package: $name"
    }
}

function Write-Utf8Json([string]$Path, [object]$Value) {
    $json = $Value | ConvertTo-Json -Depth 8
    [IO.File]::WriteAllText($Path, $json + [Environment]::NewLine, [Text.UTF8Encoding]::new($false))
}

function New-PackageDirectory(
    [string]$Distribution,
    [string]$PackagePath,
    [string]$DockerPath,
    [object[]]$ImageDefinitions,
    [string[]]$Sboms,
    [string[]]$Provenance,
    [string]$Manifest,
    [string]$Lock
) {
    New-Item -ItemType Directory -Path $PackagePath | Out-Null
    foreach ($directory in @("images", "sbom", "provenance", "manifests", "runtime", "validate", "checksums")) {
        New-Item -ItemType Directory -Path (Join-Path $PackagePath $directory) | Out-Null
    }

    Copy-UniqueFiles $Sboms (Join-Path $PackagePath "sbom") "SBOM"
    if ($Provenance.Count -gt 0) {
        Copy-UniqueFiles $Provenance (Join-Path $PackagePath "provenance") "Provenance"
    }
    Copy-Item -LiteralPath $Manifest -Destination (Join-Path $PackagePath "manifests\release-manifest.json")
    $packageManifestSeed = Get-Content -LiteralPath $Manifest -Raw -Encoding UTF8 | ConvertFrom-Json
    Copy-Item -LiteralPath $Lock -Destination (Join-Path $PackagePath "manifests\versions.release-freeze.yaml")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\README.md") -Destination (Join-Path $PackagePath "README.md")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\Validate-OfflinePackage.ps1") -Destination (Join-Path $PackagePath "validate\Validate-OfflinePackage.ps1")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\validate-offline-package.sh") -Destination (Join-Path $PackagePath "validate\validate-offline-package.sh")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\Test-OfflineRestorePreflight.ps1") -Destination (Join-Path $PackagePath "validate\Test-OfflineRestorePreflight.ps1")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\test-offline-restore-preflight.sh") -Destination (Join-Path $PackagePath "validate\test-offline-restore-preflight.sh")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\Test-OfflinePackageProtection.ps1") -Destination (Join-Path $PackagePath "validate\Test-OfflinePackageProtection.ps1")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\Test-OfflineImageArchiveContent.ps1") -Destination (Join-Path $PackagePath "validate\Test-OfflineImageArchiveContent.ps1")
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\Test-OfflinePackageSmoke.ps1") -Destination (Join-Path $PackagePath "validate\Test-OfflinePackageSmoke.ps1")

    $runtimeFiles = @(
        "compose.postgres.yaml",
        ".env.example",
        "config/platform.example.yaml",
        "config/parameter-constraints.v1.json",
        "postgres/init/010-create-service-roles.sh",
        "runbooks/backup-recovery.md",
        "runbooks/code-cut-deployment-manual.md",
        "runbooks/database-acl.md",
        "scripts/Initialize-LocalSecrets.ps1",
        "scripts/Test-DockerDesktopReadiness.ps1",
        "scripts/DockerCli.ps1"
    )
    foreach ($runtimeFile in $runtimeFiles) {
        Copy-RuntimeFile $runtimeFile (Join-Path $PackagePath "runtime")
    }
    foreach ($packageScript in @("Start-OfflinePackage.ps1", "Stop-OfflinePackage.ps1", "Test-OfflineRuntimeConfig.ps1")) {
        Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\$packageScript") -Destination (Join-Path $PackagePath "runtime\scripts\$packageScript")
    }
    Copy-Item -LiteralPath (Join-Path $deployRoot "offline\package\OPERATOR_MANUAL.md") -Destination (Join-Path $PackagePath "runtime\runbooks\offline-package-operator-manual.md")

    $runtimeEnvironmentPath = Join-Path $PackagePath "runtime\.env.example"
    $runtimeEnvironment = Get-Content -LiteralPath $runtimeEnvironmentPath -Raw -Encoding UTF8
    $referenceByName = @{}
    foreach ($image in $ImageDefinitions) { $referenceByName[$image.Name] = $image.Reference }
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^POSTGRES_IMAGE=.*$', "POSTGRES_IMAGE=$($referenceByName['postgres'])")
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^WEB_API_IMAGE=.*$', "WEB_API_IMAGE=$($referenceByName['web-api'])")
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^ALGORITHM_WORKER_IMAGE=.*$', "ALGORITHM_WORKER_IMAGE=$($referenceByName['algorithm-worker'])")
    $workerDigest = ($referenceByName['algorithm-worker'] -split '@', 2)[1]
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^WORKER_IMAGE_DIGEST=.*$', "WORKER_IMAGE_DIGEST=$workerDigest")
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^VCS_REF=.*$', "VCS_REF=$($packageManifestSeed.release.source_revision)")
    $localRuntimeEnvironment = Get-Content -LiteralPath (Join-Path $deployRoot ".runtime.env") -Raw -Encoding UTF8
    foreach ($baseImageVariable in @("GO_BUILDER_IMAGE", "NODE_BUILDER_IMAGE", "WEB_RUNTIME_IMAGE", "PYTHON_BUILDER_IMAGE", "PYTHON_RUNTIME_IMAGE")) {
        $match = [regex]::Match($localRuntimeEnvironment, "(?m)^$baseImageVariable=(?<value>[^\r\n]+)$")
        if (-not $match.Success -or $match.Groups['value'].Value -notmatch '^.+:[^@]+@sha256:[a-f0-9]{64}$') {
            throw "A required locked build image is unavailable for the portable package template."
        }
        $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, "(?m)^$baseImageVariable=.*$", "$baseImageVariable=$($match.Groups['value'].Value)")
    }
    [IO.File]::WriteAllText($runtimeEnvironmentPath, $runtimeEnvironment, [Text.UTF8Encoding]::new($false))

    foreach ($image in $ImageDefinitions) {
        $archivePath = Join-Path $PackagePath ("images\{0}.tar" -f $image.Name)
        $inspectPath = Join-Path $PackagePath ("manifests\{0}.image-inspect.json" -f $image.Name)
        & $DockerPath image inspect $image.Reference | Set-Content -LiteralPath $inspectPath -Encoding UTF8 -NoNewline
        if ($LASTEXITCODE -ne 0) {
            throw "Could not collect local image inspection evidence: $($image.Reference)"
        }
        & $DockerPath image save --output $archivePath $image.Reference
        if ($LASTEXITCODE -ne 0) {
            throw "docker image save failed: $($image.Reference)"
        }
    }

    $packageManifestPath = Join-Path $PackagePath "manifests\release-manifest.json"
    $packageManifest = Get-Content -LiteralPath $packageManifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
    foreach ($image in $ImageDefinitions) {
        $manifestImage = @($packageManifest.images | Where-Object { $_.name -eq $image.Name })
        if ($manifestImage.Count -ne 1 -or $manifestImage[0].release_reference -ne $image.Reference) {
            throw "Package release manifest image identity does not match the requested local image."
        }
        $archivePath = Join-Path $PackagePath ("images\{0}.tar" -f $image.Name)
        $manifestImage[0].archive_sha256 = (Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant()
        $manifestImage[0] | Add-Member -NotePropertyName inspect -NotePropertyValue ("manifests\{0}.image-inspect.json" -f $image.Name) -Force
        $sbomRelativePath = [string]$manifestImage[0].sbom
        if ($sbomRelativePath -notmatch '^sbom/[A-Za-z0-9._-]+\.json$') {
            throw "Package release manifest has an unsafe SBOM path."
        }
        $sbomPath = Join-Path $PackagePath $sbomRelativePath
        if (-not (Test-Path -LiteralPath $sbomPath -PathType Leaf)) {
            throw "Package release manifest references a missing SBOM."
        }
        $manifestImage[0].sbom_sha256 = (Get-FileHash -LiteralPath $sbomPath -Algorithm SHA256).Hash.ToLowerInvariant()
    }
    Write-Utf8Json $packageManifestPath $packageManifest

    $inventoryPath = Join-Path $PackagePath "manifests\inventory.json"
    $inventoryFiles = @(Get-ChildItem -LiteralPath $PackagePath -File -Recurse |
        Where-Object { $_.FullName -notlike (Join-Path $PackagePath "checksums\*") } |
        ForEach-Object {
            [pscustomobject]@{
                path = Get-RelativePath $PackagePath $_.FullName
                size_bytes = $_.Length
                sha256 = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            }
        } |
        Sort-Object path)
    Write-Utf8Json $inventoryPath ([pscustomobject]@{
        schema_version = "offline-package-inventory.v1"
        release_id = $ReleaseId
        distribution_layout = $Distribution
        target_platform = "linux/amd64"
        images = @($ImageDefinitions | ForEach-Object { [pscustomobject]@{ name = $_.Name; release_reference = $_.Reference } })
        files = $inventoryFiles
    })

    $checksumPath = Join-Path $PackagePath "checksums\sha256sum.txt"
    $checksumLines = @(Get-ChildItem -LiteralPath $PackagePath -File -Recurse |
        Where-Object { $_.FullName -ne $checksumPath } |
        ForEach-Object {
            $relativePath = Get-RelativePath $PackagePath $_.FullName
            $hash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
            "$hash  $relativePath"
        } |
        Sort-Object)
    [IO.File]::WriteAllText($checksumPath, (($checksumLines -join "`n") + "`n"), [Text.UTF8Encoding]::new($false))
}

$releaseLock = Resolve-InputFile $ReleaseLockPath "Release lock"
$releaseManifest = Resolve-InputFile $ReleaseManifestPath "Release manifest"
$manifestText = Get-Content -LiteralPath $releaseManifest -Raw -Encoding UTF8
try {
    $manifest = $manifestText | ConvertFrom-Json
} catch {
    throw "Release manifest is not valid JSON."
}
if ($manifest.release.status -ne "frozen" -or [string]::IsNullOrWhiteSpace($manifest.release.release_id)) {
    throw "Release manifest must declare a frozen, non-template release."
}
if (@($manifest.images).Count -ne 3) {
    throw "Release manifest must declare PostgreSQL, Web/API, and Algorithm Worker images."
}
$lockText = Get-Content -LiteralPath $releaseLock -Raw -Encoding UTF8
if ($lockText -notmatch '(?m)^\s*status:\s*frozen\s*$') {
    throw "Release lock is not frozen. Local package export is not authorised."
}
if ($lockText -match '(?m):\s*null\s*$' -or $lockText -match 'RELEASE_FREEZE_REQUIRED') {
    throw "Release lock still contains incomplete release inputs."
}

$images = @()
$images += [pscustomobject]@{ Name = "postgres"; Reference = [string]$PostgresImageReference }
$images += [pscustomobject]@{ Name = "web-api"; Reference = [string]$WebApiImageReference }
$images += [pscustomobject]@{ Name = "algorithm-worker"; Reference = [string]$WorkerImageReference }
foreach ($image in $images) {
    $imageReference = [string]$image.Reference
    $referenceParts = $imageReference -split '@', 2
    $hasExactTag = $referenceParts.Count -eq 2 -and $referenceParts[0] -match '^.+:[^@]+$'
    $hasSha256Digest = $referenceParts.Count -eq 2 -and $referenceParts[1] -match '^sha256:[a-f0-9]{64}$'
    if (-not $hasExactTag -or -not $hasSha256Digest) {
        throw "Every runtime image reference must have an exact tag and SHA-256 digest: $($image.Name)"
    }
    $manifestImage = @($manifest.images | Where-Object { $_.name -eq $image.Name })
    if ($manifestImage.Count -ne 1 -or [string]$manifestImage[0].release_reference -ne $imageReference) {
        throw "Release manifest image identity does not match the requested local image: $($image.Name)"
    }
    if ([string]$manifestImage[0].sbom -notmatch '^sbom/[A-Za-z0-9._-]+\.json$') {
        throw "Release manifest SBOM path is invalid: $($image.Name)"
    }
}
if ($SbomPath.Count -lt 3) {
    throw "A portable package requires SBOM inputs for PostgreSQL, Web/API, and Algorithm Worker."
}
foreach ($path in $ProvenancePath) {
    Assert-SafeProvenanceInput $path
}

$outputBase = [IO.Path]::GetFullPath($OutputRoot)
$targets = @(
    [pscustomobject]@{ Distribution = "dockerhub"; Path = (Join-Path $outputBase (Join-Path "dockerhub" $ReleaseId)) },
    [pscustomobject]@{ Distribution = "zenodo"; Path = (Join-Path $outputBase (Join-Path "zenodo" $ReleaseId)) }
)
if ($TargetDistribution -ne "both") {
    $targets = @($targets | Where-Object { $_.Distribution -eq $TargetDistribution })
}
foreach ($target in $targets) {
    if (Test-Path -LiteralPath $target.Path) {
        throw "Package target already exists; refuse to overwrite: $($target.Path)"
    }
}

$docker = Resolve-DockerExecutable $DockerExecutable
foreach ($image in $images) {
    & $docker image inspect $image.Reference *> $null
    if ($LASTEXITCODE -ne 0) {
        throw "Required local image is absent: $($image.Reference)"
    }
}

foreach ($target in $targets) {
    New-PackageDirectory $target.Distribution $target.Path $docker $images $SbomPath $ProvenancePath $releaseManifest $releaseLock
    Write-Output "Local $($target.Distribution) package exported: $($target.Path)"
}
Write-Output "No Docker Hub, Zenodo, registry, or network action was performed."
