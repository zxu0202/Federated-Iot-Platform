[CmdletBinding()]
param(
    [string]$DockerExecutable = "docker",
    [int]$MinimumNcpu = 4,
    [int]$MinimumMemoryGiB = 3
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

if ($MinimumNcpu -lt 1 -or $MinimumMemoryGiB -lt 1) {
    throw "Minimum resource values must be positive."
}

function Add-ReadinessError([System.Collections.Generic.List[string]]$Errors, [string]$Message) {
    $Errors.Add($Message)
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
        throw "Docker CLI is not visible in this process PATH. Supply -DockerExecutable with the absolute path to docker.exe."
    }
    return $command.Source
}

$errors = [System.Collections.Generic.List[string]]::new()
$docker = Resolve-DockerExecutable $DockerExecutable

try {
    $version = & $docker version --format '{{json .}}' 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw ($version -join [Environment]::NewLine)
    }
    $versionData = $version | ConvertFrom-Json
} catch {
    Add-ReadinessError $errors "Docker client/server version query failed: $($_.Exception.Message)"
    $versionData = $null
}

try {
    $composeVersion = & $docker compose version --short 2>&1
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace(($composeVersion -join "").Trim())) {
        throw ($composeVersion -join [Environment]::NewLine)
    }
} catch {
    Add-ReadinessError $errors "Docker Compose v2 plugin is unavailable: $($_.Exception.Message)"
    $composeVersion = $null
}

try {
    $info = & $docker info --format '{{json .}}' 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw ($info -join [Environment]::NewLine)
    }
    $infoData = $info | ConvertFrom-Json
} catch {
    Add-ReadinessError $errors "Docker daemon info query failed: $($_.Exception.Message)"
    $infoData = $null
}

try {
    $networkLines = @(& $docker network ls --format '{{json .}}' 2>&1)
    if ($LASTEXITCODE -ne 0) {
        throw ($networkLines -join [Environment]::NewLine)
    }
    $networks = @($networkLines | Where-Object { -not [string]::IsNullOrWhiteSpace($_) } | ForEach-Object { $_ | ConvertFrom-Json })
} catch {
    Add-ReadinessError $errors "Docker network inventory query failed: $($_.Exception.Message)"
    $networks = @()
}

if ($null -ne $infoData) {
    if ($infoData.OSType -ne "linux") {
        Add-ReadinessError $errors "Docker must use Linux containers; daemon OSType is '$($infoData.OSType)'."
    }
    if ([int64]$infoData.NCPU -lt $MinimumNcpu) {
        Add-ReadinessError $errors "Docker has $($infoData.NCPU) CPUs; this Compose baseline requires at least $MinimumNcpu."
    }
    $minimumMemoryBytes = [int64]$MinimumMemoryGiB * 1GB
    if ([int64]$infoData.MemTotal -lt $minimumMemoryBytes) {
        Add-ReadinessError $errors "Docker has $([math]::Round([int64]$infoData.MemTotal / 1GB, 2)) GiB memory; this Compose baseline requires at least $MinimumMemoryGiB GiB."
    }
    if ($infoData.IPv4Forwarding -ne $true) {
        Add-ReadinessError $errors "Docker daemon IPv4 forwarding is disabled."
    }
}

if ($networks.Count -gt 0 -and @($networks | Where-Object { $_.Name -eq "bridge" -and $_.Driver -eq "bridge" -and $_.Scope -eq "local" }).Count -ne 1) {
    Add-ReadinessError $errors "Docker default local bridge network is unavailable."
}

$hostIpv4Candidates = @()
try {
    $hostIpv4Candidates = @(Get-NetIPAddress -AddressFamily IPv4 -ErrorAction Stop |
        Where-Object {
            $_.AddressState -eq "Preferred" -and
            $_.IPAddress -notlike "127.*" -and
            $_.IPAddress -notlike "169.254.*"
        } |
        Sort-Object InterfaceIndex, IPAddress |
        Select-Object -ExpandProperty IPAddress)
    if ($hostIpv4Candidates.Count -eq 0) {
        Add-ReadinessError $errors "No usable host IPv4 address is available for the Web/API port binding."
    }
} catch {
    Add-ReadinessError $errors "Host IPv4 inventory query failed: $($_.Exception.Message)"
}

[pscustomobject]@{
    DockerExecutable = $docker
    ClientVersion = if ($null -ne $versionData) { $versionData.Client.Version } else { $null }
    ServerVersion = if ($null -ne $versionData) { $versionData.Server.Version } else { $null }
    ComposeVersion = if ($null -ne $composeVersion) { ($composeVersion -join "").Trim() } else { $null }
    OSType = if ($null -ne $infoData) { $infoData.OSType } else { $null }
    OperatingSystem = if ($null -ne $infoData) { $infoData.OperatingSystem } else { $null }
    NCPU = if ($null -ne $infoData) { $infoData.NCPU } else { $null }
    MemoryGiB = if ($null -ne $infoData) { [math]::Round([int64]$infoData.MemTotal / 1GB, 2) } else { $null }
    IPv4Forwarding = if ($null -ne $infoData) { $infoData.IPv4Forwarding } else { $null }
    Networks = ($networks | ForEach-Object { "$($_.Name):$($_.Driver):$($_.Scope)" }) -join ", "
    HostIpv4CandidateCount = $hostIpv4Candidates.Count
    Result = if ($errors.Count -eq 0) { "READY" } else { "NOT_READY" }
} | Format-List

if ($errors.Count -gt 0) {
    $errors | ForEach-Object { Write-Error $_ -ErrorAction Continue }
    throw "Docker Desktop readiness validation failed. No Compose operation was performed."
}

Write-Output "Docker Desktop readiness validation passed. No Compose operation was performed."
