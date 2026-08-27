[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$PackageDirectory,
    [Parameter(Mandatory)]
    [string]$EnvironmentFile,
    [Parameter(Mandatory)]
    [string]$ProjectName,
    [Parameter(Mandatory)]
    [string]$DockerExecutable
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
$environmentPath = (Resolve-Path -LiteralPath $EnvironmentFile).Path
& (Join-Path $PSScriptRoot "Validate-OfflinePackage.ps1") -PackageDirectory $packageRoot
& (Join-Path $PSScriptRoot "Test-OfflineRuntimeConfig.ps1") -PackageDirectory $packageRoot -EnvironmentFile $environmentPath
$composePath = Join-Path $packageRoot "runtime\compose.postgres.yaml"
& $DockerExecutable compose --env-file $environmentPath -f $composePath --project-name $ProjectName config --quiet
if ($LASTEXITCODE -ne 0) { throw "Offline package Compose configuration validation failed." }
& $DockerExecutable compose --env-file $environmentPath -f $composePath --project-name $ProjectName up --detach --no-build --pull never
if ($LASTEXITCODE -ne 0) { throw "Offline package startup failed." }
Write-Output "Offline package startup requested."
