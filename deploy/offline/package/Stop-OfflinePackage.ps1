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
$composePath = Join-Path $packageRoot "runtime\compose.postgres.yaml"
& $DockerExecutable compose --env-file $environmentPath -f $composePath --project-name $ProjectName stop --timeout 30
if ($LASTEXITCODE -ne 0) { throw "Offline package stop failed." }
Write-Output "Offline package stopped. Persistent volumes were retained."
