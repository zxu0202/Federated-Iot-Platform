[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$PackageDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
& (Join-Path $PSScriptRoot "Validate-OfflinePackage.ps1") -PackageDirectory $packageRoot
& (Join-Path $PSScriptRoot "Test-OfflinePackageProtection.ps1") -PackageDirectory $packageRoot
& (Join-Path $PSScriptRoot "Test-OfflineImageArchiveContent.ps1") -PackageDirectory $packageRoot

foreach ($relativePath in @(
    "runtime/runbooks/backup-recovery.md",
    "runtime/compose.postgres.yaml",
    "runtime/postgres/init/010-create-service-roles.sh",
    "images/postgres.tar",
    "images/web-api.tar",
    "images/algorithm-worker.tar"
)) {
    if (-not (Test-Path -LiteralPath (Join-Path $packageRoot $relativePath) -PathType Leaf)) {
        throw "Restore preflight asset is missing: $relativePath"
    }
}

Write-Output "Offline restore preflight passed. Restore only into fresh isolated volumes using runtime/runbooks/backup-recovery.md."
