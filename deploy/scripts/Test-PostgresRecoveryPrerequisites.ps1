[CmdletBinding()]
param(
    [Parameter(Mandatory = $true)]
    [string]$BaseUrl,
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\\.env"),
    [string]$ConfigFile = (Join-Path $PSScriptRoot "..\\config\\platform.yaml"),
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\\compose.postgres.yaml"),
    [string]$ProjectName = "federated-iot-platform"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

& (Join-Path $PSScriptRoot "Test-DeploymentConfig.ps1") -EnvironmentFile $EnvironmentFile -ConfigFile $ConfigFile

$runningServices = @(& docker compose --env-file $EnvironmentFile -f $ComposeFile --project-name $ProjectName ps --status running --services)
foreach ($service in @("postgres", "web-api", "algorithm-worker")) {
    if ($runningServices -notcontains $service) {
        throw "Recovery prerequisite failed: required service is not running."
    }
}

$response = Invoke-RestMethod -Method Get -Uri ("{0}/api/v1/health/ready" -f $BaseUrl.TrimEnd('/')) -TimeoutSec 10
if ($response.status -ne "ready") {
    throw "Recovery prerequisite failed: Web/API readiness is not ready."
}
if ($response.checks.database_profile -ne "postgres") {
    throw "Recovery prerequisite failed: readiness does not report the PostgreSQL profile."
}
foreach ($check in @("database", "schema", "dataset_store", "artifact_store", "reference_profile", "network_binding", "worker")) {
    if ($response.checks.$check -ne "ok") {
        throw "Recovery prerequisite failed: a required readiness check is not ok."
    }
}

Write-Output "PostgreSQL backup and recovery prerequisites passed. Run the isolated fresh-volume recovery drill described in runbooks/backup-recovery.md."
