[CmdletBinding()]
param(
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\\.env"),
    [string]$ConfigFile = (Join-Path $PSScriptRoot "..\\config\\platform.yaml"),
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\\compose.postgres.yaml"),
    [string]$ProjectName = "federated-iot-platform",
    [string]$DockerExecutable = "docker",
    [string]$BindAddress,
    [string]$BindInterface,
    [switch]$Build,
    [switch]$ConnectedSourceBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "DockerCli.ps1")

function Read-DotEnv([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        if ($trimmed -notmatch "^([A-Za-z_][A-Za-z0-9_]*)=(.*)$") { throw "Invalid .env entry." }
        $values[$Matches[1]] = $Matches[2].Trim().Trim('"')
    }
    return $values
}

function Test-UsableIPv4([string]$Address) {
    $parsed = $null
    if (-not [Net.IPAddress]::TryParse($Address, [ref]$parsed)) { return $false }
    if ($parsed.AddressFamily -ne [Net.Sockets.AddressFamily]::InterNetwork) { return $false }
    $bytes = $parsed.GetAddressBytes()
    return $bytes[0] -ne 0 -and $bytes[0] -ne 127 -and -not ($bytes[0] -eq 169 -and $bytes[1] -eq 254)
}

function Get-InterfaceIPv4([string]$InterfaceAlias) {
    $addresses = Get-NetIPAddress -InterfaceAlias $InterfaceAlias -AddressFamily IPv4 -ErrorAction SilentlyContinue |
        Where-Object { Test-UsableIPv4 $_.IPAddress } |
        Sort-Object InterfaceIndex, AddressFamily, IPAddress
    if ($null -ne $addresses -and @($addresses).Count -gt 0) { return @($addresses)[0].IPAddress }
    return $null
}

if ($ConnectedSourceBuild -and $Build) {
    throw "Connected source images must be built by Build-ConnectedSourceImages.ps1 before startup; do not combine ConnectedSourceBuild with Build."
}
$validationArguments = @{
    EnvironmentFile = $EnvironmentFile
    ConfigFile = $ConfigFile
    ComposeFile = $ComposeFile
}
if ($ConnectedSourceBuild) { $validationArguments.ConnectedSourceBuild = $true }
& (Join-Path $PSScriptRoot "Test-DeploymentConfig.ps1") @validationArguments
$docker = Resolve-DockerExecutable -RequestedExecutable $DockerExecutable
$envValues = Read-DotEnv $EnvironmentFile
if ($ConnectedSourceBuild) {
    $ProjectName = $envValues["COMPOSE_PROJECT_NAME"]
    foreach ($name in @("WEB_API_IMAGE", "ALGORITHM_WORKER_IMAGE", "POSTGRES_IMAGE")) {
        Invoke-DockerCommand -DockerExecutable $docker -Arguments @("image", "inspect", $envValues[$name]) | Out-Null
    }
}

$selectedAddress = $BindAddress
if ([string]::IsNullOrWhiteSpace($selectedAddress) -and -not [string]::IsNullOrWhiteSpace($BindInterface)) {
    $selectedAddress = Get-InterfaceIPv4 $BindInterface
    if (-not $selectedAddress) {
        throw "BindInterface did not resolve to a usable IPv4 address."
    }
}
if ([string]::IsNullOrWhiteSpace($selectedAddress) -and $envValues.ContainsKey("PLATFORM_BIND_ADDRESS")) {
    $selectedAddress = $envValues["PLATFORM_BIND_ADDRESS"]
}
if ([string]::IsNullOrWhiteSpace($selectedAddress)) {
    $interfaceNames = @()
    if ($envValues.ContainsKey("PLATFORM_BIND_INTERFACE") -and -not [string]::IsNullOrWhiteSpace($envValues["PLATFORM_BIND_INTERFACE"])) {
        $interfaceNames += $envValues["PLATFORM_BIND_INTERFACE"]
    } elseif ($envValues.ContainsKey("PLATFORM_CANDIDATE_INTERFACES") -and -not [string]::IsNullOrWhiteSpace($envValues["PLATFORM_CANDIDATE_INTERFACES"])) {
        $interfaceNames += $envValues["PLATFORM_CANDIDATE_INTERFACES"].Split(',') | ForEach-Object { $_.Trim() } | Where-Object { $_ }
    }
    foreach ($interfaceName in $interfaceNames) {
        $selectedAddress = Get-InterfaceIPv4 $interfaceName
        if ($selectedAddress) { break }
    }
    if ($interfaceNames.Count -gt 0 -and -not $selectedAddress) {
        throw "Configured host binding interface did not resolve to a usable IPv4 address."
    }
}
if ([string]::IsNullOrWhiteSpace($selectedAddress)) { $selectedAddress = "0.0.0.0" }
if ($selectedAddress -ne "0.0.0.0" -and (-not (Test-UsableIPv4 $selectedAddress) -or -not (Get-NetIPAddress -IPAddress $selectedAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue))) {
    throw "PLATFORM_BIND_ADDRESS must be 0.0.0.0 or a usable IPv4 address assigned to this host."
}

$env:PLATFORM_BIND_ADDRESS = $selectedAddress
try {
    Invoke-DockerCommand -DockerExecutable $docker -Arguments @("compose", "--env-file", $EnvironmentFile, "-f", $ComposeFile, "--project-name", $ProjectName, "config", "--quiet")
} catch {
    throw "Docker Compose configuration validation failed: $($_.Exception.Message)"
}

$composeArguments = @("compose", "--env-file", $EnvironmentFile, "-f", $ComposeFile, "--project-name", $ProjectName, "up", "--detach")
if ($ConnectedSourceBuild) {
    $composeArguments += @("--no-build", "--pull", "never")
} elseif ($Build) {
    $composeArguments += "--build"
}
try {
    Invoke-DockerCommand -DockerExecutable $docker -Arguments $composeArguments
} catch {
    throw "Docker Compose startup failed: $($_.Exception.Message)"
}

$profile = if ($ConnectedSourceBuild) { "connected-source" } else { "release-frozen" }
Write-Output "Platform startup requested with profile $profile and Web/API published on $selectedAddress."
