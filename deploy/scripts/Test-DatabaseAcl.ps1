[CmdletBinding()]
param(
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\\.env"),
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\\compose.postgres.yaml"),
    [string]$ProjectName = "federated-iot-platform",
    [string]$DockerExecutable = "docker"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

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

$assertionFile = Join-Path $PSScriptRoot "..\\postgres\\verify\\acl-assertions.sql"
foreach ($path in @($EnvironmentFile, $ComposeFile, $assertionFile)) {
    if (-not (Test-Path -LiteralPath $path -PathType Leaf)) {
        throw "Required ACL verification input is missing: $path"
    }
}

$envValues = Read-DotEnv $EnvironmentFile
$database = if ($envValues.ContainsKey("POSTGRES_DB")) { $envValues["POSTGRES_DB"] } else { "federated_iot" }
$administrator = if ($envValues.ContainsKey("POSTGRES_ADMIN_USER")) { $envValues["POSTGRES_ADMIN_USER"] } else { "platform_admin" }
$webApiUser = if ($envValues.ContainsKey("WEB_API_DB_USER")) { $envValues["WEB_API_DB_USER"] } else { "web_api" }
$workerUser = if ($envValues.ContainsKey("WORKER_DB_USER")) { $envValues["WORKER_DB_USER"] } else { "algorithm_worker" }
$composeArguments = @("compose", "--env-file", $EnvironmentFile, "-f", $ComposeFile, "--project-name", $ProjectName)
$postgresContainer = (& $DockerExecutable @composeArguments "ps" "-q" "postgres").Trim()
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($postgresContainer)) {
    throw "The PostgreSQL Compose service is not running for project $ProjectName."
}

Get-Content -LiteralPath $assertionFile -Raw -Encoding UTF8 |
    & $DockerExecutable exec -i $postgresContainer psql -X -v ON_ERROR_STOP=1 -U $administrator -d $database
if ($LASTEXITCODE -ne 0) { throw "PostgreSQL ACL metadata assertions failed." }

function Invoke-ServiceCredentialQuery([string]$SecretPath, [string]$DatabaseUser, [string]$Query) {
    $credentialScript = @'
set -eu
password=$(tr -d '\r\n' < "$1")
export PGPASSWORD="$password"
exec psql -h 127.0.0.1 -X -v ON_ERROR_STOP=1 -U "$2" -d "$3" -c "$4"
'@
    $output = & $DockerExecutable exec -i $postgresContainer sh -ec $credentialScript "acl-service-credential-check" $SecretPath $DatabaseUser $database $Query 2>&1
    return [pscustomobject]@{ ExitCode = $LASTEXITCODE; Output = @($output) }
}

$directSelect = Invoke-ServiceCredentialQuery "/run/secrets/worker_db_password" $workerUser "SELECT 1 FROM worker_jobs LIMIT 1;"
if ($directSelect.ExitCode -eq 0 -or ($directSelect.Output -join "`n") -notmatch "permission denied") {
    throw "algorithm_worker direct SELECT denial was not observed."
}

$workerRecovery = Invoke-ServiceCredentialQuery "/run/secrets/worker_db_password" $workerUser "SELECT worker_recover_expired_leases();"
if ($workerRecovery.ExitCode -eq 0 -or ($workerRecovery.Output -join "`n") -notmatch "permission denied") {
    throw "algorithm_worker recovery-function execution denial was not observed."
}

$webApiRecovery = Invoke-ServiceCredentialQuery "/run/secrets/web_api_db_password" $webApiUser "BEGIN; SELECT worker_recover_expired_leases() AS web_api_execution_proof; ROLLBACK;"
if ($webApiRecovery.ExitCode -ne 0) {
    throw "web_api could not execute worker_recover_expired_leases with its service credential."
}
$webApiRecovery.Output | Write-Output
Write-Output "PostgreSQL ACL gate passed: Worker direct SELECT and recovery execution were denied; Web/API recovery execution was rolled back."
