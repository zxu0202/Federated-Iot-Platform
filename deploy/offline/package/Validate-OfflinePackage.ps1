[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$PackageDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-PackageFile([string]$Root, [string]$RelativePath) {
    if ([IO.Path]::IsPathRooted($RelativePath) -or $RelativePath -match '(^|[\\/])\.\.([\\/]|$)') {
        throw "Package path is unsafe: $RelativePath"
    }
    $path = Join-Path $Root $RelativePath
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required package file is missing: $RelativePath"
    }
    return (Resolve-Path -LiteralPath $path).Path
}

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
$requiredFiles = @(
    "README.md",
    "images/postgres.tar",
    "images/web-api.tar",
    "images/algorithm-worker.tar",
    "manifests/release-manifest.json",
    "manifests/versions.release-freeze.yaml",
    "manifests/inventory.json",
    "runtime/compose.postgres.yaml",
    "runtime/.env.example",
    "runtime/config/platform.example.yaml",
    "runtime/config/parameter-constraints.v1.json",
    "runtime/postgres/init/010-create-service-roles.sh",
    "runtime/scripts/Initialize-LocalSecrets.ps1",
    "runtime/scripts/Test-DockerDesktopReadiness.ps1",
    "runtime/scripts/DockerCli.ps1",
    "runtime/scripts/Start-OfflinePackage.ps1",
    "runtime/scripts/Stop-OfflinePackage.ps1",
    "runtime/scripts/Test-OfflineRuntimeConfig.ps1",
    "runtime/runbooks/offline-package-operator-manual.md",
    "validate/Validate-OfflinePackage.ps1",
    "validate/validate-offline-package.sh",
    "validate/Test-OfflineRestorePreflight.ps1",
    "validate/test-offline-restore-preflight.sh",
    "validate/Test-OfflinePackageProtection.ps1",
    "validate/Test-OfflineImageArchiveContent.ps1",
    "validate/Test-OfflinePackageSmoke.ps1",
    "checksums/sha256sum.txt"
)
foreach ($relativePath in $requiredFiles) {
    [void](Resolve-PackageFile $packageRoot $relativePath)
}

$manifestPath = Resolve-PackageFile $packageRoot "manifests/release-manifest.json"
try {
    $manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
} catch {
    throw "Release manifest is not valid JSON."
}
if ($manifest.release.status -ne "frozen" -or [string]::IsNullOrWhiteSpace($manifest.release.release_id)) {
    throw "Release manifest must declare a frozen, non-template release."
}
if (@($manifest.images).Count -ne 3) {
    throw "Release manifest must declare exactly PostgreSQL, Web/API, and Algorithm Worker images."
}
foreach ($expectedName in @("postgres", "web-api", "algorithm-worker")) {
    $image = @($manifest.images | Where-Object { $_.name -eq $expectedName })
    if ($image.Count -ne 1 -or [string]$image[0].release_reference -notmatch '^.+:[^@]+@sha256:[a-f0-9]{64}$') {
        throw "Release manifest must contain one immutable $expectedName image reference."
    }
    foreach ($propertyName in @("archive", "archive_sha256", "sbom", "sbom_sha256", "inspect")) {
        if ([string]::IsNullOrWhiteSpace([string]$image[0].$propertyName)) {
            throw "Release manifest image metadata is incomplete: $expectedName."
        }
    }
    $archivePath = Resolve-PackageFile $packageRoot ([string]$image[0].archive)
    if ((Get-FileHash -LiteralPath $archivePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$image[0].archive_sha256) {
        throw "Image archive does not match the release manifest: $expectedName."
    }
    $sbomPath = Resolve-PackageFile $packageRoot ([string]$image[0].sbom)
    if ((Get-FileHash -LiteralPath $sbomPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$image[0].sbom_sha256) {
        throw "Image SBOM does not match the release manifest: $expectedName."
    }
    try {
        $sbom = Get-Content -LiteralPath $sbomPath -Raw -Encoding UTF8 | ConvertFrom-Json
        if ($sbom.spdxVersion -ne "SPDX-2.3") { throw "unexpected SPDX version" }
    } catch {
        throw "Image SBOM is not valid SPDX JSON: $expectedName."
    }
    [void](Resolve-PackageFile $packageRoot ([string]$image[0].inspect))
}
$expectedConstraintsHash = [string]$manifest.runtime.parameter_constraints_sha256
if ($expectedConstraintsHash -notmatch '^[a-f0-9]{64}$') {
    throw "Release manifest must bind runtime parameter constraints to a SHA-256 value."
}
$constraintsPath = Resolve-PackageFile $packageRoot "runtime/config/parameter-constraints.v1.json"
$actualConstraintsHash = (Get-FileHash -LiteralPath $constraintsPath -Algorithm SHA256).Hash.ToLowerInvariant()
if ($actualConstraintsHash -ne $expectedConstraintsHash) {
    throw "Runtime parameter constraints do not match the frozen release manifest."
}

$lock = Get-Content -LiteralPath (Resolve-PackageFile $packageRoot "manifests/versions.release-freeze.yaml") -Raw -Encoding UTF8
if ($lock -notmatch '(?m)^\s*status:\s*frozen\s*$') {
    throw "Package release lock is not frozen."
}

$checksumPath = Resolve-PackageFile $packageRoot "checksums/sha256sum.txt"
$records = Get-Content -LiteralPath $checksumPath -Encoding UTF8
if ($records.Count -eq 0) {
    throw "Checksum manifest is empty."
}
foreach ($record in $records) {
    if ($record -notmatch '^(?<hash>[a-f0-9]{64})  (?<path>.+)$') {
        throw "Checksum record has an invalid format."
    }
    $filePath = Resolve-PackageFile $packageRoot $Matches.path
    $actual = (Get-FileHash -LiteralPath $filePath -Algorithm SHA256).Hash.ToLowerInvariant()
    if ($actual -ne $Matches.hash) {
        throw "Checksum mismatch: $($Matches.path)"
    }
}

$inventoryPath = Resolve-PackageFile $packageRoot "manifests/inventory.json"
try {
    $inventory = Get-Content -LiteralPath $inventoryPath -Raw -Encoding UTF8 | ConvertFrom-Json
} catch {
    throw "Package inventory is not valid JSON."
}
if ($inventory.release_id -ne $manifest.release.release_id -or @($inventory.files).Count -eq 0) {
    throw "Package inventory does not match the frozen release manifest."
}
foreach ($entry in @($inventory.files)) {
    $filePath = Resolve-PackageFile $packageRoot ([string]$entry.path)
    if ((Get-FileHash -LiteralPath $filePath -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$entry.sha256) {
        throw "Package inventory checksum mismatch: $($entry.path)"
    }
}

Write-Output "Offline package validation passed. Image loading and runtime deployment remain separate operator actions."
