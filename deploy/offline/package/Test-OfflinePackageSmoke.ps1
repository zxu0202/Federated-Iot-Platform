[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$PackageDirectory,
    [Parameter(Mandatory)]
    [string]$DockerExecutable,
    [int]$HostApiPort = 18081
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Convert-ToComposePath([string]$Path) {
    return ([IO.Path]::GetFullPath($Path).Replace('\', '/'))
}
function Set-DotEnvValue([string]$Path, [string]$Name, [string]$Value) {
    $content = Get-Content -LiteralPath $Path -Raw -Encoding UTF8
    $updated = [regex]::Replace($content, "(?m)^$Name=.*$", "$Name=$Value")
    if ($updated -eq $content) { throw "Offline smoke environment template is missing a required setting." }
    [IO.File]::WriteAllText($Path, $updated, [Text.UTF8Encoding]::new($false))
}
function Wait-ServiceState([string]$Docker, [string[]]$ComposeArguments, [string]$Service, [string]$ExpectedState, [int]$TimeoutSeconds) {
    $deadline = [DateTime]::UtcNow.AddSeconds($TimeoutSeconds)
    do {
        $id = (& $Docker @ComposeArguments ps -q $Service).Trim()
        if ($id) {
            $state = (& $Docker inspect $id --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}').Trim()
            if ($state -eq $ExpectedState) { return }
        }
        Start-Sleep -Seconds 2
    } while ([DateTime]::UtcNow -lt $deadline)
    throw "Offline smoke service did not reach the expected state."
}

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
$smokeId = [Guid]::NewGuid().ToString("N")
$projectName = "federated-iot-package-smoke-$smokeId"
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) "federated-iot-package-smoke-$smokeId"
$environmentPath = Join-Path $temporaryRoot ".env"
$composePath = Join-Path $packageRoot "runtime\compose.postgres.yaml"
$composeArguments = @("compose", "--env-file", $environmentPath, "-f", $composePath, "--project-name", $projectName)

try {
    [IO.Directory]::CreateDirectory($temporaryRoot) | Out-Null
    Copy-Item -LiteralPath (Join-Path $packageRoot "runtime\.env.example") -Destination $environmentPath
    Copy-Item -LiteralPath (Join-Path $packageRoot "runtime\config\platform.example.yaml") -Destination (Join-Path $temporaryRoot "platform.yaml")
    Copy-Item -LiteralPath (Join-Path $packageRoot "runtime\config\parameter-constraints.v1.json") -Destination (Join-Path $temporaryRoot "parameter-constraints.v1.json")
    & (Join-Path $packageRoot "runtime\scripts\Initialize-LocalSecrets.ps1") -OutputDirectory (Join-Path $temporaryRoot "secrets")
    Set-DotEnvValue $environmentPath "COMPOSE_PROJECT_NAME" $projectName
    Set-DotEnvValue $environmentPath "PLATFORM_CONFIG_PATH" (Convert-ToComposePath (Join-Path $temporaryRoot "platform.yaml"))
    Set-DotEnvValue $environmentPath "PARAMETER_CONSTRAINTS_SOURCE" (Convert-ToComposePath (Join-Path $temporaryRoot "parameter-constraints.v1.json"))
    Set-DotEnvValue $environmentPath "POSTGRES_ADMIN_PASSWORD_SOURCE" (Convert-ToComposePath (Join-Path $temporaryRoot "secrets\postgres_admin_password.txt"))
    Set-DotEnvValue $environmentPath "WEB_API_DB_PASSWORD_SOURCE" (Convert-ToComposePath (Join-Path $temporaryRoot "secrets\web_api_db_password.txt"))
    Set-DotEnvValue $environmentPath "MIGRATOR_DB_PASSWORD_SOURCE" (Convert-ToComposePath (Join-Path $temporaryRoot "secrets\platform_migrator_db_password.txt"))
    Set-DotEnvValue $environmentPath "WORKER_DB_PASSWORD_SOURCE" (Convert-ToComposePath (Join-Path $temporaryRoot "secrets\algorithm_worker_db_password.txt"))
    Set-DotEnvValue $environmentPath "PLATFORM_BIND_ADDRESS" "127.0.0.1"
    Set-DotEnvValue $environmentPath "HOST_API_PORT" $HostApiPort

    foreach ($archive in @("postgres.tar", "web-api.tar", "algorithm-worker.tar")) {
        & $DockerExecutable load --input (Join-Path $packageRoot "images\$archive")
        if ($LASTEXITCODE -ne 0) { throw "Offline image archive load failed." }
    }
    & (Join-Path $packageRoot "runtime\scripts\Start-OfflinePackage.ps1") -PackageDirectory $packageRoot -EnvironmentFile $environmentPath -ProjectName $projectName -DockerExecutable $DockerExecutable
    Wait-ServiceState $DockerExecutable $composeArguments "postgres" "healthy" 120
    Wait-ServiceState $DockerExecutable $composeArguments "web-api" "healthy" 120
    Wait-ServiceState $DockerExecutable $composeArguments "algorithm-worker" "healthy" 120
    $migrationId = (& $DockerExecutable @composeArguments ps -q migration).Trim()
    if (-not $migrationId -or (& $DockerExecutable inspect $migrationId --format '{{.State.ExitCode}}').Trim() -ne "0") {
        throw "Offline smoke migration did not exit successfully."
    }
    foreach ($path in @("/api/v1/health/live", "/api/v1/health/ready")) {
        $response = Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$HostApiPort$path" -TimeoutSec 15
        if ($response.StatusCode -ne 200) { throw "Offline smoke health endpoint did not return HTTP 200." }
    }
    Write-Output "Offline package smoke validation passed."
} finally {
    if (Test-Path -LiteralPath $environmentPath -PathType Leaf) {
        & $DockerExecutable @composeArguments down --volumes --remove-orphans *> $null
    }
    if (Test-Path -LiteralPath $temporaryRoot) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}
