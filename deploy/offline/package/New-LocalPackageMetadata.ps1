[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$OutputDirectory,
    [Parameter(Mandatory)]
    [ValidatePattern('^[A-Za-z0-9][A-Za-z0-9._-]*$')]
    [string]$ReleaseId,
    [Parameter(Mandatory)]
    [string]$PostgresImageReference,
    [Parameter(Mandatory)]
    [string]$WebApiImageReference,
    [Parameter(Mandatory)]
    [string]$WorkerImageReference,
    [Parameter(Mandatory)]
    [string]$PostgresSbomPath,
    [Parameter(Mandatory)]
    [string]$WebApiSbomPath,
    [Parameter(Mandatory)]
    [string]$WorkerSbomPath,
    [Parameter(Mandatory)]
    [string]$ParameterConstraintsPath,
    [Parameter(Mandatory)]
    [string]$ComposeSha256,
    [Parameter(Mandatory)]
    [string]$ConfigSha256,
    [Parameter(Mandatory)]
    [string]$WebRevision,
    [Parameter(Mandatory)]
    [string]$WorkerRevision
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-Sha256([string]$Path) {
    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) { throw "Required local package input is missing." }
    return (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()
}
function Write-Utf8Json([string]$Path, [object]$Value) {
    [IO.File]::WriteAllText($Path, (($Value | ConvertTo-Json -Depth 10) + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
}

foreach ($reference in @($PostgresImageReference, $WebApiImageReference, $WorkerImageReference)) {
    if ($reference -notmatch '^.+:[^@]+@sha256:[a-f0-9]{64}$') { throw "A local package image reference is not immutable." }
}
foreach ($hash in @($ComposeSha256, $ConfigSha256)) {
    if ($hash -notmatch '^[a-f0-9]{64}$') { throw "A supplied frozen SHA-256 value is invalid." }
}
[IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null

$sboms = @(
    [ordered]@{ name = "postgres"; path = $PostgresSbomPath; reference = $PostgresImageReference },
    [ordered]@{ name = "web-api"; path = $WebApiSbomPath; reference = $WebApiImageReference },
    [ordered]@{ name = "algorithm-worker"; path = $WorkerSbomPath; reference = $WorkerImageReference }
)
$images = @($sboms | ForEach-Object {
    [ordered]@{
        name = $_.name
        release_reference = $_.reference
        archive = "images/$($_.name).tar"
        archive_sha256 = "pending-export"
        sbom = "sbom/$([IO.Path]::GetFileName($_.path))"
        sbom_sha256 = Get-Sha256 $_.path
    }
})
$manifest = [ordered]@{
    schema_version = "offline-release-manifest.v1"
    release = [ordered]@{
        release_id = $ReleaseId
        status = "frozen"
        scope = "local-package-only"
        external_version = "1.0.0-m1"
        source_revision = "$WebRevision+$WorkerRevision"
        target_platform = "linux/amd64"
    }
    images = $images
    runtime = [ordered]@{
        compose = "runtime/compose.postgres.yaml"
        compose_sha256 = $ComposeSha256
        parameter_constraints_sha256 = Get-Sha256 $ParameterConstraintsPath
        deployment_config_sha256 = $ConfigSha256
        restore_validator = "validate/Test-OfflineRestorePreflight.ps1"
    }
    distribution = [ordered]@{
        docker_hub = [ordered]@{ mode = "human-manual-only" }
        zenodo = [ordered]@{ mode = "human-manual-only" }
    }
    checksums = [ordered]@{ algorithm = "sha256"; manifest = "checksums/sha256sum.txt"; inventory = "manifests/inventory.json" }
}
$lock = @"
schema_version: local-package-lock.v1
release:
  status: frozen
  scope: local-package-only
  release_id: $ReleaseId
  external_version: 1.0.0-m1
  target_platform: linux/amd64
  external_publication: not-authorized
images:
  postgres:
    local_runtime_reference: $PostgresImageReference
  web_api:
    local_runtime_reference: $WebApiImageReference
    source_revision: $WebRevision
  algorithm_worker:
    local_runtime_reference: $WorkerImageReference
    source_revision: $WorkerRevision
runtime:
  compose_sha256: $ComposeSha256
  deployment_config_sha256: $ConfigSha256
"@
Write-Utf8Json (Join-Path $OutputDirectory "release-manifest.json") $manifest
[IO.File]::WriteAllText((Join-Path $OutputDirectory "versions.release-freeze.yaml"), $lock, [Text.UTF8Encoding]::new($false))
Write-Output "Local package metadata written."
