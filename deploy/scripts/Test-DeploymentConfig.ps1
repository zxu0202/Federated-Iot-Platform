[CmdletBinding()]
param(
    [string]$EnvironmentFile = (Join-Path $PSScriptRoot "..\\.env"),
    [string]$ConfigFile = (Join-Path $PSScriptRoot "..\\config\\platform.yaml"),
    [string]$VersionLockFile = (Join-Path $PSScriptRoot "..\\versions.release-freeze.yaml"),
    [string]$ComposeFile = (Join-Path $PSScriptRoot "..\\compose.postgres.yaml"),
    [string]$BackendConstraintsFile = (Join-Path $PSScriptRoot "..\\..\\backend\\config\\parameter-constraints.v1.json"),
    [switch]$ConnectedSourceBuild
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Read-DotEnv([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        if ($trimmed -notmatch "^([A-Za-z_][A-Za-z0-9_]*)=(.*)$") {
            throw "Invalid .env entry."
        }
        $value = $Matches[2].Trim()
        if ($value.Length -ge 2 -and $value.StartsWith('"') -and $value.EndsWith('"')) {
            $value = $value.Substring(1, $value.Length - 2)
        }
        $values[$Matches[1]] = $value
    }
    return $values
}

function Add-ValidationError([System.Collections.Generic.List[string]]$Errors, [string]$Message) {
    $Errors.Add($Message)
}

$errors = [System.Collections.Generic.List[string]]::new()
foreach ($file in @($EnvironmentFile, $ConfigFile, $VersionLockFile, $ComposeFile, $BackendConstraintsFile)) {
    if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
        Add-ValidationError $errors "Required deployment input is missing: $([IO.Path]::GetFileName($file))."
    }
}
if ($errors.Count -gt 0) {
    $errors | ForEach-Object { Write-Error $_ -ErrorAction Continue }
    throw "Deployment validation failed."
}

$envValues = Read-DotEnv $EnvironmentFile
$expectedRoleNames = @{ MIGRATOR_DB_USER = "platform_migrator"; WORKER_REPOSITORY_OWNER = "platform_worker_repository_owner" }
foreach ($name in $expectedRoleNames.Keys) {
    if ($envValues.ContainsKey($name) -and $envValues[$name] -ne $expectedRoleNames[$name]) {
        Add-ValidationError $errors "$name is fixed by the PostgreSQL ACL contract."
    }
}
$digestReference = '^.+:[^@]+@sha256:[a-f0-9]{64}$'
foreach ($name in @("POSTGRES_IMAGE", "GO_BUILDER_IMAGE", "NODE_BUILDER_IMAGE", "WEB_RUNTIME_IMAGE", "PYTHON_BUILDER_IMAGE", "PYTHON_RUNTIME_IMAGE")) {
    if (-not $envValues.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($envValues[$name]) -or $envValues[$name] -eq "RELEASE_FREEZE_REQUIRED") {
        Add-ValidationError $errors "$name is still blocked by the release-freeze gate."
        continue
    }
    if ($envValues[$name] -notmatch $digestReference) {
        Add-ValidationError $errors "$name must contain an exact tag and sha256 digest."
    }
}
$lock = Get-Content -LiteralPath $VersionLockFile -Raw -Encoding UTF8
if ($ConnectedSourceBuild) {
    if (-not $envValues.ContainsKey("BUILD_PROFILE") -or $envValues["BUILD_PROFILE"] -ne "connected-source") {
        Add-ValidationError $errors "Connected source startup requires BUILD_PROFILE=connected-source."
    }
    if (-not $envValues.ContainsKey("COMPOSE_PROJECT_NAME") -or $envValues["COMPOSE_PROJECT_NAME"] -ne "federated-iot-platform-connected") {
        Add-ValidationError $errors "Connected source startup requires the isolated federated-iot-platform-connected Compose project name."
    }
    foreach ($name in @("POSTGRES_IMAGE", "GO_BUILDER_IMAGE", "NODE_BUILDER_IMAGE", "WEB_RUNTIME_IMAGE", "PYTHON_BUILDER_IMAGE", "PYTHON_RUNTIME_IMAGE")) {
        if ($envValues.ContainsKey($name) -and $lock -notmatch "(?m)^\s*required_reference:\s*$([regex]::Escape($envValues[$name]))\s*$") {
            Add-ValidationError $errors "$name does not match an approved digest-pinned base image in versions.release-freeze.yaml."
        }
    }
    $connectedApplicationTags = @{
        WEB_API_IMAGE = '^zx/federated-iot-platform:1\.0\.0-m1-connected-[a-f0-9]{12}$'
        ALGORITHM_WORKER_IMAGE = '^zx/federated-iot-platform-worker:1\.0\.0-m1-connected-[a-f0-9]{12}$'
    }
    foreach ($name in $connectedApplicationTags.Keys) {
        if (-not $envValues.ContainsKey($name) -or $envValues[$name] -notmatch $connectedApplicationTags[$name]) {
            Add-ValidationError $errors "$name must use the dedicated 1.0.0-m1-connected-<source-sha> local tag."
        }
    }
    if (-not $envValues.ContainsKey("WORKER_IMAGE_DIGEST") -or $envValues["WORKER_IMAGE_DIGEST"] -notmatch '^sha256:[a-f0-9]{64}$') {
        Add-ValidationError $errors "WORKER_IMAGE_DIGEST must be the BuildKit manifest digest generated for the connected Worker image."
    }
    if (-not $envValues.ContainsKey("VCS_REF") -or $envValues["VCS_REF"] -notmatch '^[a-f0-9]{40}([a-f0-9]{24})?$') {
        Add-ValidationError $errors "VCS_REF must be the full clean Git source revision for a connected source build."
    }
} else {
    if ($envValues.ContainsKey("BUILD_PROFILE") -and $envValues["BUILD_PROFILE"] -eq "connected-source") {
        Add-ValidationError $errors "Connected source environments require the explicit ConnectedSourceBuild startup mode."
    }
    foreach ($name in @("WEB_API_IMAGE", "ALGORITHM_WORKER_IMAGE")) {
        if (-not $envValues.ContainsKey($name) -or $envValues[$name] -notmatch $digestReference) {
            Add-ValidationError $errors "$name must contain an exact release tag and sha256 digest after release freeze."
        }
    }
    if ($lock -notmatch '(?m)^\s*status:\s*frozen\s*$') {
        Add-ValidationError $errors "versions.release-freeze.yaml is not frozen; release image digests have not been verified."
    }
}
if ($envValues.ContainsKey("POSTGRES_IMAGE") -and $envValues["POSTGRES_IMAGE"] -match '(^|:)latest(@|$)') {
    Add-ValidationError $errors "POSTGRES_IMAGE must not use the floating latest tag."
}
$resolvedSecretPaths = @{}
foreach ($name in @("POSTGRES_ADMIN_PASSWORD_SOURCE", "WEB_API_DB_PASSWORD_SOURCE", "MIGRATOR_DB_PASSWORD_SOURCE", "WORKER_DB_PASSWORD_SOURCE")) {
    if (-not $envValues.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($envValues[$name])) {
        Add-ValidationError $errors "$name must point to a local secret file."
        continue
    }
    $secretPath = $envValues[$name]
    if (-not [IO.Path]::IsPathRooted($secretPath)) {
        $secretPath = Join-Path (Split-Path -Parent $EnvironmentFile) $secretPath
    }
    if (-not (Test-Path -LiteralPath $secretPath -PathType Leaf)) {
        Add-ValidationError $errors "$name does not reference an available local secret file."
        continue
    }
    $resolvedSecretPaths[$name] = [IO.Path]::GetFullPath($secretPath)
}
if ($resolvedSecretPaths.Count -eq 4 -and @($resolvedSecretPaths.Values | Select-Object -Unique).Count -ne 4) {
    Add-ValidationError $errors "PostgreSQL administrator, migrator, Web/API, and Worker must use four distinct local secret files."
}

if (-not $envValues.ContainsKey("PARAMETER_CONSTRAINTS_SOURCE") -or [string]::IsNullOrWhiteSpace($envValues["PARAMETER_CONSTRAINTS_SOURCE"])) {
    Add-ValidationError $errors "PARAMETER_CONSTRAINTS_SOURCE must point to a local parameter constraints JSON file."
} else {
    $constraintsPath = $envValues["PARAMETER_CONSTRAINTS_SOURCE"]
    if (-not [IO.Path]::IsPathRooted($constraintsPath)) {
        $constraintsPath = Join-Path (Split-Path -Parent $EnvironmentFile) $constraintsPath
    }
    if (-not (Test-Path -LiteralPath $constraintsPath -PathType Leaf)) {
        Add-ValidationError $errors "PARAMETER_CONSTRAINTS_SOURCE does not reference an available local parameter constraints JSON file."
    } else {
        try {
            $constraints = Get-Content -LiteralPath $constraintsPath -Raw -Encoding UTF8 | ConvertFrom-Json
            $backendConstraints = Get-Content -LiteralPath $BackendConstraintsFile -Raw -Encoding UTF8 | ConvertFrom-Json
            $constraintsHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $constraintsPath).Hash.ToLowerInvariant()
            $backendConstraintsHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $BackendConstraintsFile).Hash.ToLowerInvariant()
            if ($constraintsHash -ne $backendConstraintsHash) {
                Add-ValidationError $errors "PARAMETER_CONSTRAINTS_SOURCE SHA-256 does not match Backend's parameter-constraints.v1.json."
            }
            if ($constraints.contract_version -ne "parameter-constraints.v1") {
                Add-ValidationError $errors "Parameter constraints must use contract_version parameter-constraints.v1."
            }
            $rules = @($constraints.paths.PSObject.Properties)
            if ($rules.Count -ne 69) {
                Add-ValidationError $errors "Parameter constraints must declare exactly 69 named paths."
            }
            $editableCount = 0
            $fixedCount = 0
            foreach ($ruleProperty in $rules) {
                $rule = $ruleProperty.Value
                $properties = @($rule.PSObject.Properties.Name)
                foreach ($requiredProperty in @("type", "editable", "nullable", "minimum", "maximum", "allowed_values")) {
                    if ($properties -notcontains $requiredProperty) {
                        Add-ValidationError $errors "A parameter constraint is missing $requiredProperty."
                    }
                }
                if ([string]$ruleProperty.Name -notmatch '^[a-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$') {
                    Add-ValidationError $errors "Parameter constraints contain an invalid named path."
                }
                if ($rule.editable -isnot [bool] -or $rule.nullable -isnot [bool]) {
                    Add-ValidationError $errors "Parameter constraint editable and nullable flags must be boolean."
                }
                if ($rule.editable -eq $true) {
                    $editableCount++
                } else {
                    $fixedCount++
                }
                if ([string]$rule.type -notin @("integer", "number", "boolean", "string")) {
                    Add-ValidationError $errors "Parameter constraint type is invalid."
                }
                foreach ($boundName in @("minimum", "maximum")) {
                    $bound = $rule.$boundName
                    if ($null -ne $bound -and (($bound -is [bool]) -or -not ($bound -is [IConvertible]))) {
                        Add-ValidationError $errors "Parameter constraint $boundName must be numeric or null."
                    }
                }
                if ($null -ne $rule.minimum -and $null -ne $rule.maximum -and [double]$rule.minimum -gt [double]$rule.maximum) {
                    Add-ValidationError $errors "Parameter constraint minimum must not exceed maximum."
                }
                if ($null -ne $rule.allowed_values -and (($rule.allowed_values -is [string]) -or -not ($rule.allowed_values -is [System.Collections.IEnumerable]))) {
                    Add-ValidationError $errors "Parameter constraint allowed_values must be an array or null."
                }
            }
            if ($editableCount -ne 67 -or $fixedCount -ne 2) {
                Add-ValidationError $errors "Parameter constraints must contain exactly 67 editable and 2 fixed paths."
            }
            $fixedRules = @{
                "split.agent_count" = @{ Type = "integer"; AllowedValue = 3 }
                "global_surrogate.leave_one_out" = @{ Type = "boolean"; AllowedValue = $true }
            }
            foreach ($fixedPath in $fixedRules.Keys) {
                $fixedRule = $constraints.paths.PSObject.Properties[$fixedPath].Value
                if ($null -eq $fixedRule -or $fixedRule.editable -ne $false -or $fixedRule.type -ne $fixedRules[$fixedPath].Type -or @($fixedRule.allowed_values).Count -ne 1 -or @($fixedRule.allowed_values)[0] -ne $fixedRules[$fixedPath].AllowedValue) {
                    Add-ValidationError $errors "Parameter constraints fixed path $fixedPath does not match the S1 topology contract."
                }
            }
        } catch {
            Add-ValidationError $errors "PARAMETER_CONSTRAINTS_SOURCE is not valid UTF-8 JSON with the expected parameter-constraints.v1 shape."
        }
    }
}

$compose = Get-Content -LiteralPath $ComposeFile -Raw -Encoding UTF8
if ($compose -notmatch 'IOT_PARAMETER_CONSTRAINTS_FILE:\s*/etc/federated-iot/parameter-constraints\.v1\.json') {
    Add-ValidationError $errors "Compose must expose the absolute Web/API parameter constraints file path."
}
if ($compose -notmatch '(?s)source:\s+"\$\{PARAMETER_CONSTRAINTS_SOURCE:\?[^}]+\}".*?target:\s+/etc/federated-iot/parameter-constraints\.v1\.json\s+read_only:\s+true') {
    Add-ValidationError $errors "Compose must mount parameter constraints read-only into Web/API."
}
$webApiSection = [regex]::Match($compose, '(?ms)^  web-api:\s*\r?\n(?<section>.*?)(?=^  [A-Za-z0-9_-]+:\s*$|\z)').Groups['section'].Value
$workerSection = [regex]::Match($compose, '(?ms)^  algorithm-worker:\s*\r?\n(?<section>.*?)(?=^networks:\s*$|\z)').Groups['section'].Value
if ([string]::IsNullOrWhiteSpace($webApiSection) -or [string]::IsNullOrWhiteSpace($workerSection)) {
    Add-ValidationError $errors "Compose must define Web/API and Algorithm Worker service sections."
} else {
    $databaseRelativePrefix = "runs/"
    $canonicalCommittedPath = "runs/run_contract_fixture/committed/artifact_manifest.json"
    $expectedArtifactPath = "/var/lib/iot/runs/run_contract_fixture/committed/artifact_manifest.json"
    $resolvedArtifactPath = "/var/lib/iot/" + $canonicalCommittedPath
    if (-not $canonicalCommittedPath.StartsWith($databaseRelativePrefix, [StringComparison]::Ordinal) -or $resolvedArtifactPath -ne $expectedArtifactPath) {
        Add-ValidationError $errors "Artifact namespace contract must resolve database runs/ relative paths from /var/lib/iot."
    }
    if ($webApiSection -notmatch '(?m)^\s{6}IOT_ARTIFACT_ROOT:\s*/var/lib/iot\s*$') {
        Add-ValidationError $errors "Web/API IOT_ARTIFACT_ROOT must be the controlled /var/lib/iot namespace root."
    }
    if ($webApiSection -notmatch '(?m)^\s{6}IOT_HTTP_ADDRESS:\s*0\.0\.0\.0:8080\s*$') {
        Add-ValidationError $errors "Web/API must listen on 0.0.0.0:8080 inside the container network."
    }
    if ($webApiSection -notmatch '(?m)^\s*-\s*"\$\{PLATFORM_BIND_ADDRESS:-0\.0\.0\.0\}:\$\{HOST_API_PORT:-8080\}:8080"\s*$') {
        Add-ValidationError $errors 'Web/API host publishing must default to 0.0.0.0:${HOST_API_PORT}:8080.'
    }
    if ($webApiSection -match '(?m)^\s{6}IOT_ARTIFACT_ROOT:\s*/var/lib/iot/runs\s*$') {
        Add-ValidationError $errors "Web/API IOT_ARTIFACT_ROOT must not include the database runs/ relative prefix."
    }
    if ($webApiSection -notmatch '(?m)^\s*-\s*artifacts:/var/lib/iot/runs:ro\s*$') {
        Add-ValidationError $errors "Web/API must mount artifacts at /var/lib/iot/runs read-only."
    }
    if ($webApiSection -notmatch '(?m)^\s*-\s*datasets:/var/lib/iot/datasets\s*$') {
        Add-ValidationError $errors "Web/API must retain the datasets mount at /var/lib/iot/datasets."
    }
    if ($workerSection -notmatch '(?m)^\s*-\s*artifacts:/var/lib/iot/runs\s*$') {
        Add-ValidationError $errors "Algorithm Worker must share the writable artifacts volume at /var/lib/iot/runs."
    }
}

$config = Get-Content -LiteralPath $ConfigFile -Raw -Encoding UTF8
if ($config -notmatch '(?m)^\s*profile:\s*postgres\s*$') {
    Add-ValidationError $errors "platform.yaml must select the PostgreSQL deployment profile."
}
if ($config -match '(?im)^\s*profile:\s*sqlite\s*$') {
    Add-ValidationError $errors "SQLite is not authorised for the first S1 closed loop."
}
foreach ($requiredKey in @("host_binding:", "allowed_hosts:", "field_standards:", "zl:", "sd:", "simulation_running_slots:", "simulation_waiting_slots:", "preflight_waiting_slots:", "worker_pool_capacity:")) {
    if ($config -notmatch [regex]::Escape($requiredKey)) {
        Add-ValidationError $errors "platform.yaml is missing required configuration section $requiredKey"
    }
}
if ($config -match '(?im)^\s*validation_enabled:\s*true\s*$' -and $config -match '(?im)^\s*(unit_symbol|standard_reference|minimum|maximum|expected_period_ms|tolerance_ms):\s*null\s*$') {
    Add-ValidationError $errors "Field validation is enabled while a field-standard value remains null."
}

if ($errors.Count -gt 0) {
    $errors | ForEach-Object { Write-Error $_ -ErrorAction Continue }
    throw "Deployment validation failed. No containers were started."
}

Write-Output "Deployment configuration passed static validation."
