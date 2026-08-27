[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$PackageDirectory,
    [Parameter(Mandatory)]
    [string]$EnvironmentFile
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Read-DotEnv([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        if ($trimmed -notmatch "^([A-Za-z_][A-Za-z0-9_]*)=(.*)$") { throw "Operator environment file contains an invalid entry." }
        $values[$Matches[1]] = $Matches[2].Trim().Trim('"')
    }
    return $values
}
function Resolve-OperatorPath([string]$Value, [string]$EnvironmentPath) {
    if ([IO.Path]::IsPathRooted($Value)) { return [IO.Path]::GetFullPath($Value) }
    return [IO.Path]::GetFullPath((Join-Path (Split-Path -Parent $EnvironmentPath) $Value))
}

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
$environmentPath = (Resolve-Path -LiteralPath $EnvironmentFile).Path
$manifestPath = Join-Path $packageRoot "manifests\release-manifest.json"
if (-not (Test-Path -LiteralPath $manifestPath -PathType Leaf)) { throw "Package release manifest is missing." }
$manifest = Get-Content -LiteralPath $manifestPath -Raw -Encoding UTF8 | ConvertFrom-Json
$values = Read-DotEnv $environmentPath
foreach ($name in @("POSTGRES_IMAGE", "WEB_API_IMAGE", "ALGORITHM_WORKER_IMAGE", "PLATFORM_CONFIG_PATH", "PARAMETER_CONSTRAINTS_SOURCE")) {
    if (-not $values.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($values[$name])) { throw "Operator environment file is missing a required setting." }
}
foreach ($image in @($manifest.images)) {
    $envName = switch ($image.name) { "postgres" { "POSTGRES_IMAGE" }; "web-api" { "WEB_API_IMAGE" }; "algorithm-worker" { "ALGORITHM_WORKER_IMAGE" }; default { throw "Package manifest contains an unknown image." } }
    if ($values[$envName] -ne $image.release_reference) { throw "Operator image reference does not match the frozen package manifest." }
}
$constraintsPath = Resolve-OperatorPath $values["PARAMETER_CONSTRAINTS_SOURCE"] $environmentPath
if (-not (Test-Path -LiteralPath $constraintsPath -PathType Leaf)) { throw "Operator parameter constraints file is missing." }
if ((Get-FileHash -LiteralPath $constraintsPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne [string]$manifest.runtime.parameter_constraints_sha256) { throw "Operator parameter constraints do not match the frozen package manifest." }
$platformPath = Resolve-OperatorPath $values["PLATFORM_CONFIG_PATH"] $environmentPath
if (-not (Test-Path -LiteralPath $platformPath -PathType Leaf)) { throw "Operator platform configuration file is missing." }
$secretPaths = [System.Collections.Generic.List[string]]::new()
foreach ($name in @("POSTGRES_ADMIN_PASSWORD_SOURCE", "WEB_API_DB_PASSWORD_SOURCE", "MIGRATOR_DB_PASSWORD_SOURCE", "WORKER_DB_PASSWORD_SOURCE")) {
    if (-not $values.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($values[$name])) { throw "Operator environment file is missing a secret-file path." }
    $secretPath = Resolve-OperatorPath $values[$name] $environmentPath
    if (-not (Test-Path -LiteralPath $secretPath -PathType Leaf)) { throw "An operator secret file is missing." }
    [void]$secretPaths.Add($secretPath)
}
if (@($secretPaths | Select-Object -Unique).Count -ne 4) { throw "Operator secret-file paths must be distinct." }
$bindAddress = if ($values.ContainsKey("PLATFORM_BIND_ADDRESS")) { $values["PLATFORM_BIND_ADDRESS"] } else { "0.0.0.0" }
$parsedAddress = $null
if ($bindAddress -ne "0.0.0.0" -and -not [Net.IPAddress]::TryParse($bindAddress, [ref]$parsedAddress)) { throw "PLATFORM_BIND_ADDRESS must be 0.0.0.0 or an IPv4 address." }
Write-Output "Offline runtime configuration validation passed."
