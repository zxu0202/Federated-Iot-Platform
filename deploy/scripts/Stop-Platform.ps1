[CmdletBinding()]
param(
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\\.env"),
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\\compose.postgres.yaml"),
    [string]$ProjectName = "federated-iot-platform",
    [string]$DockerExecutable = "docker",
    [switch]$ConnectedSourceBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "DockerCli.ps1")

if ($ConnectedSourceBuild) { $ProjectName = "federated-iot-platform-connected" }
$docker = Resolve-DockerExecutable -RequestedExecutable $DockerExecutable
try {
    Invoke-DockerCommand -DockerExecutable $docker -Arguments @("compose", "--env-file", $EnvironmentFile, "-f", $ComposeFile, "--project-name", $ProjectName, "stop", "--timeout", "30")
} catch {
    throw "Docker Compose stop failed: $($_.Exception.Message)"
}
Write-Output "Platform stop completed. Persistent volumes were retained."
