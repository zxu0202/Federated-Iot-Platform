[CmdletBinding()]
param(
    [string]$SourceRoot = (Join-Path $PSScriptRoot "..\.."),
    [string]$EnvironmentTemplate = (Join-Path $PSScriptRoot "..\connected-build.env.example"),
    [string]$OutputEnvironmentFile = (Join-Path $PSScriptRoot "..\.env.connected.runtime"),
    [string]$DockerExecutable = "docker"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"
. (Join-Path $PSScriptRoot "DockerCli.ps1")

function Read-DotEnv([string]$Path) {
    $values = @{}
    foreach ($line in Get-Content -LiteralPath $Path -Encoding UTF8) {
        $trimmed = $line.Trim()
        if ($trimmed.Length -eq 0 -or $trimmed.StartsWith("#")) { continue }
        if ($trimmed -notmatch "^([A-Za-z_][A-Za-z0-9_]*)=(.*)$") { throw "Invalid connected build environment entry." }
        $values[$Matches[1]] = $Matches[2].Trim().Trim('"')
    }
    return $values
}

function Get-BuildDigest([string]$MetadataPath) {
    if (-not (Test-Path -LiteralPath $MetadataPath -PathType Leaf)) { throw "BuildKit metadata file is missing." }
    $metadata = Get-Content -LiteralPath $MetadataPath -Raw -Encoding UTF8 | ConvertFrom-Json
    $property = $metadata.PSObject.Properties["containerimage.digest"]
    if ($null -eq $property -or [string]$property.Value -notmatch '^sha256:[a-f0-9]{64}$') {
        throw "BuildKit metadata does not contain a valid containerimage.digest."
    }
    return [string]$property.Value
}

function Invoke-SourceImageBuild(
    [string]$Docker,
    [string]$Context,
    [string]$Dockerfile,
    [string]$Tag,
    [string]$MetadataPath,
    [hashtable]$BuildArguments
) {
    $arguments = @(
        "buildx", "build", "--load", "--pull", "--provenance=false",
        "--platform", "linux/amd64", "--file", $Dockerfile,
        "--tag", $Tag, "--metadata-file", $MetadataPath
    )
    foreach ($name in @($BuildArguments.Keys | Sort-Object)) {
        $arguments += @("--build-arg", "$name=$($BuildArguments[$name])")
    }
    $arguments += $Context
    Invoke-DockerCommand -DockerExecutable $Docker -Arguments $arguments
}

function Test-ImageIdentity(
    [string]$Docker,
    [string]$Tag,
    [string]$ExpectedUser,
    [string]$ExpectedRevision,
    [string]$ExpectedVersion
) {
    $raw = (Invoke-DockerCommand -DockerExecutable $Docker -Arguments @("image", "inspect", $Tag, "--format", "{{json .}}") | Out-String).Trim()
    $image = $raw | ConvertFrom-Json
    if ($image.Os -ne "linux" -or $image.Architecture -ne "amd64") { throw "Connected image platform identity is invalid: $Tag" }
    if ($image.Config.User -ne $ExpectedUser) { throw "Connected image runtime user is invalid: $Tag" }
    if ($image.Config.Labels.'org.opencontainers.image.revision' -ne $ExpectedRevision) { throw "Connected image source revision label is invalid: $Tag" }
    if ($image.Config.Labels.'org.opencontainers.image.version' -ne $ExpectedVersion) { throw "Connected image version label is invalid: $Tag" }
    if ($image.Config.Labels.'org.opencontainers.image.source-build-profile' -ne "connected-source") { throw "Connected image build profile label is invalid: $Tag" }
}

$sourcePath = (Resolve-Path -LiteralPath $SourceRoot).Path
$templatePath = (Resolve-Path -LiteralPath $EnvironmentTemplate).Path
$outputPath = [IO.Path]::GetFullPath($OutputEnvironmentFile)
if ($outputPath -eq $templatePath) { throw "The generated runtime environment must not overwrite its template." }
if (Test-Path -LiteralPath $outputPath) { throw "The generated runtime environment already exists; review and remove it before rebuilding." }

$git = Get-Command git -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
if ($null -eq $git) { throw "Git is required to bind the connected images to a public source revision." }
$sourceRevision = (& $git.Source -C $sourcePath rev-parse --verify HEAD).Trim()
if ($LASTEXITCODE -ne 0 -or $sourceRevision -notmatch '^[a-f0-9]{40}([a-f0-9]{24})?$') { throw "The source checkout does not have a full immutable Git revision." }
$worktreeStatus = @(& $git.Source -C $sourcePath status --porcelain --untracked-files=all)
if ($LASTEXITCODE -ne 0) { throw "The source checkout status could not be verified." }
if ($worktreeStatus.Count -ne 0) { throw "Connected images require a clean committed source checkout; uncommitted files would make the VCS label false." }
$sourceRevisionSuffix = $sourceRevision.Substring(0, 12)

$values = Read-DotEnv $templatePath
$required = @(
    "BUILD_PROFILE", "POSTGRES_IMAGE", "GO_BUILDER_IMAGE", "NODE_BUILDER_IMAGE", "WEB_RUNTIME_IMAGE",
    "PYTHON_BUILDER_IMAGE", "PYTHON_RUNTIME_IMAGE", "CONNECTED_GOPROXY", "CONNECTED_GOSUMDB",
    "CONNECTED_NPM_REGISTRY", "CONNECTED_PYPI_INDEX_URL", "WEB_API_IMAGE", "ALGORITHM_WORKER_IMAGE",
    "BUILD_VERSION", "BACKEND_VERSION", "FRONTEND_VERSION", "ALGORITHM_VERSION", "WORKER_VERSION",
    "PYTHON_PACKAGE_VERSION"
)
foreach ($name in $required) {
    if (-not $values.ContainsKey($name) -or [string]::IsNullOrWhiteSpace($values[$name])) { throw "Connected build template is missing $name." }
}
if ($values.BUILD_PROFILE -ne "connected-source") { throw "Connected build template has an invalid BUILD_PROFILE." }
$webImageTag = "$($values.WEB_API_IMAGE)-$sourceRevisionSuffix"
$workerImageTag = "$($values.ALGORITHM_WORKER_IMAGE)-$sourceRevisionSuffix"
$digestReference = '^.+:[^@]+@sha256:[a-f0-9]{64}$'
foreach ($name in @("POSTGRES_IMAGE", "GO_BUILDER_IMAGE", "NODE_BUILDER_IMAGE", "WEB_RUNTIME_IMAGE", "PYTHON_BUILDER_IMAGE", "PYTHON_RUNTIME_IMAGE")) {
    if ($values[$name] -notmatch $digestReference) { throw "$name must use an exact tag and sha256 digest." }
}
foreach ($name in @("CONNECTED_GOPROXY", "CONNECTED_NPM_REGISTRY", "CONNECTED_PYPI_INDEX_URL")) {
    if ($values[$name] -match '://[^/\s]*@') { throw "$name must not contain embedded credentials." }
}

$docker = Resolve-DockerExecutable -RequestedExecutable $DockerExecutable
Invoke-DockerCommand -DockerExecutable $docker -Arguments @("buildx", "version")
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ("federated-iot-connected-build-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
try {
    $webMetadata = Join-Path $temporaryRoot "web-api.metadata.json"
    $workerMetadata = Join-Path $temporaryRoot "algorithm-worker.metadata.json"
    $webBuildParameters = @{
        Docker = $docker
        Context = $sourcePath
        Dockerfile = (Join-Path $sourcePath "deploy\Dockerfile.web-api.connected")
        Tag = $webImageTag
        MetadataPath = $webMetadata
        BuildArguments = @{
            GO_BUILDER_IMAGE = $values.GO_BUILDER_IMAGE
            NODE_BUILDER_IMAGE = $values.NODE_BUILDER_IMAGE
            WEB_RUNTIME_IMAGE = $values.WEB_RUNTIME_IMAGE
            GOPROXY = $values.CONNECTED_GOPROXY
            GOSUMDB = $values.CONNECTED_GOSUMDB
            NPM_REGISTRY = $values.CONNECTED_NPM_REGISTRY
            BUILD_VERSION = $values.BUILD_VERSION
            BACKEND_VERSION = $values.BACKEND_VERSION
            FRONTEND_VERSION = $values.FRONTEND_VERSION
            VCS_REF = $sourceRevision
        }
    }
    Invoke-SourceImageBuild @webBuildParameters
    $webDigest = Get-BuildDigest $webMetadata
    $webIdentityParameters = @{
        Docker = $docker
        Tag = $webImageTag
        ExpectedUser = "webapi:platform"
        ExpectedRevision = $sourceRevision
        ExpectedVersion = $values.BUILD_VERSION
    }
    Test-ImageIdentity @webIdentityParameters

    $workerBuildParameters = @{
        Docker = $docker
        Context = $sourcePath
        Dockerfile = (Join-Path $sourcePath "deploy\Dockerfile.algorithm-worker.connected")
        Tag = $workerImageTag
        MetadataPath = $workerMetadata
        BuildArguments = @{
            PYTHON_BUILDER_IMAGE = $values.PYTHON_BUILDER_IMAGE
            PYTHON_RUNTIME_IMAGE = $values.PYTHON_RUNTIME_IMAGE
            PYPI_INDEX_URL = $values.CONNECTED_PYPI_INDEX_URL
            BUILD_VERSION = $values.BUILD_VERSION
            ALGORITHM_VERSION = $values.ALGORITHM_VERSION
            WORKER_VERSION = $values.WORKER_VERSION
            PYTHON_PACKAGE_VERSION = $values.PYTHON_PACKAGE_VERSION
            VCS_REF = $sourceRevision
        }
    }
    Invoke-SourceImageBuild @workerBuildParameters
    $workerDigest = Get-BuildDigest $workerMetadata
    $workerIdentityParameters = @{
        Docker = $docker
        Tag = $workerImageTag
        ExpectedUser = "worker:platform"
        ExpectedRevision = $sourceRevision
        ExpectedVersion = $values.BUILD_VERSION
    }
    Test-ImageIdentity @workerIdentityParameters

    Invoke-DockerCommand -DockerExecutable $docker -Arguments @("pull", "--platform", "linux/amd64", $values.POSTGRES_IMAGE)

    $runtimeEnvironment = Get-Content -LiteralPath $templatePath -Raw -Encoding UTF8
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^WEB_API_IMAGE=.*$', "WEB_API_IMAGE=$webImageTag")
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^ALGORITHM_WORKER_IMAGE=.*$', "ALGORITHM_WORKER_IMAGE=$workerImageTag")
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^VCS_REF=.*$', "VCS_REF=$sourceRevision")
    $runtimeEnvironment = [regex]::Replace($runtimeEnvironment, '(?m)^WORKER_IMAGE_DIGEST=.*$', "WORKER_IMAGE_DIGEST=$workerDigest")
    [IO.File]::WriteAllText($outputPath, $runtimeEnvironment, [Text.UTF8Encoding]::new($false))

    [pscustomobject]@{
        SourceRevision = $sourceRevision
        WebApiImage = $webImageTag
        WebApiManifestDigest = $webDigest
        WorkerImage = $workerImageTag
        WorkerManifestDigest = $workerDigest
        RuntimeEnvironment = $outputPath
        PublicationPerformed = $false
    }
} finally {
    if (Test-Path -LiteralPath $temporaryRoot) { Remove-Item -LiteralPath $temporaryRoot -Recurse -Force }
}
