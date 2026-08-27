[CmdletBinding()]
param(
    [string]$BackendDirectory = (Join-Path $PSScriptRoot "..\\..\\backend"),
    [string]$GoExecutable = "go",
    [string]$VendorTreeManifest = (Join-Path $PSScriptRoot "..\\offline\\go\\backend-vendor.tree.sha256"),
    [string]$OfflineInputsManifest = (Join-Path $PSScriptRoot "..\\offline\\go\\backend-offline-inputs.sha256"),
    [string]$ModuleCacheDirectory,
    [string]$ModuleCacheManifest
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RelativePath([string]$Root, [string]$Path) {
    return $Path.Substring($Root.Length).TrimStart('\', '/').Replace('\', '/')
}

function Test-Sha256Manifest([string]$Root, [string]$ManifestPath, [string]$ExpectedPrefix) {
    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
        throw "Offline checksum manifest is missing."
    }
    $entries = @{}
    $paths = [System.Collections.Generic.List[string]]::new()
    foreach ($line in Get-Content -LiteralPath $ManifestPath -Encoding UTF8) {
        if ($line -notmatch '^(?<hash>[a-f0-9]{64})  (?<path>[^\\/].*)$') {
            throw "Offline checksum manifest has an invalid entry."
        }
        $relativePath = $Matches.path.Replace('\', '/')
        if (-not $relativePath.StartsWith($ExpectedPrefix) -or $relativePath.Contains('../') -or $relativePath.StartsWith('../')) {
            throw "Offline checksum manifest contains an unsafe path."
        }
        if ($entries.ContainsKey($relativePath)) {
            throw "Offline checksum manifest contains a duplicate path."
        }
        $entries[$relativePath] = $Matches.hash
        $paths.Add($relativePath)
    }
    if ($entries.Count -eq 0) { throw "Offline checksum manifest is empty." }

    $sorted = @($paths | Sort-Object)
    if (@(Compare-Object -ReferenceObject $paths -DifferenceObject $sorted -SyncWindow 0).Count -ne 0) {
        throw "Offline checksum manifest paths are not sorted."
    }

    $scopePath = $Root
    if (-not [string]::IsNullOrWhiteSpace($ExpectedPrefix)) {
        $scopePath = Join-Path $Root $ExpectedPrefix.TrimEnd('/')
    }
    $actualPaths = Get-ChildItem -LiteralPath $scopePath -File -Recurse | ForEach-Object { Get-RelativePath $Root $_.FullName }
    foreach ($actualPath in $actualPaths) {
        if (-not $entries.ContainsKey($actualPath)) {
            throw "Offline checksum manifest is missing a regular file."
        }
    }
    foreach ($relativePath in $entries.Keys) {
        $fullPath = Join-Path $Root $relativePath
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
            throw "Offline checksum manifest references a missing file."
        }
        $actualHash = (Get-FileHash -LiteralPath $fullPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $entries[$relativePath]) {
            throw "Offline checksum verification failed."
        }
    }
}

function Test-DeclaredFileManifest([string]$Root, [string]$ManifestPath) {
    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
        throw "Offline input checksum manifest is missing."
    }
    $paths = @{}
    foreach ($line in Get-Content -LiteralPath $ManifestPath -Encoding UTF8) {
        if ($line -notmatch '^(?<hash>[a-f0-9]{64})  (?<path>[^\\/].*)$') {
            throw "Offline input checksum manifest has an invalid entry."
        }
        $relativePath = $Matches.path.Replace('\', '/')
        if ($relativePath.Contains('../') -or $relativePath.StartsWith('../') -or $paths.ContainsKey($relativePath)) {
            throw "Offline input checksum manifest contains an unsafe or duplicate path."
        }
        $paths[$relativePath] = $Matches.hash
    }
    if ($paths.Count -eq 0) { throw "Offline input checksum manifest is empty." }
    foreach ($relativePath in $paths.Keys) {
        $fullPath = Join-Path $Root $relativePath
        if (-not (Test-Path -LiteralPath $fullPath -PathType Leaf)) {
            throw "Offline input checksum manifest references a missing file."
        }
        $actualHash = (Get-FileHash -LiteralPath $fullPath -Algorithm SHA256).Hash.ToLowerInvariant()
        if ($actualHash -ne $paths[$relativePath]) {
            throw "Offline input checksum verification failed."
        }
    }
}

function Test-CanonicalVendorTree([string]$BackendRoot, [string]$ManifestPath) {
    if (-not (Test-Path -LiteralPath $ManifestPath -PathType Leaf)) {
        throw "Vendor tree checksum manifest is missing."
    }
    $manifestLines = @(Get-Content -LiteralPath $ManifestPath -Encoding UTF8)
    if ($manifestLines.Count -ne 1 -or $manifestLines[0] -notmatch '^(?<hash>[a-f0-9]{64})  vendor/$') {
        throw "Vendor tree checksum manifest must contain exactly one canonical vendor entry."
    }
    $expectedHash = $Matches.hash
    $lines = @(Get-ChildItem -LiteralPath (Join-Path $BackendRoot "vendor") -File -Recurse | ForEach-Object {
        $relativePath = Get-RelativePath $BackendRoot $_.FullName
        $fileHash = (Get-FileHash -LiteralPath $_.FullName -Algorithm SHA256).Hash.ToLowerInvariant()
        "$fileHash  $relativePath"
    } | Sort-Object)
    if ($lines.Count -eq 0) { throw "Vendor tree is empty." }
    $payload = [Text.Encoding]::UTF8.GetBytes(($lines -join "`n") + "`n")
    $sha256 = [Security.Cryptography.SHA256]::Create()
    try {
        $actualHash = [Convert]::ToHexString($sha256.ComputeHash($payload)).ToLowerInvariant()
    } finally {
        $sha256.Dispose()
    }
    if ($actualHash -ne $expectedHash) {
        throw "Vendor tree checksum verification failed."
    }
}

$backendPath = (Resolve-Path -LiteralPath $BackendDirectory).Path
foreach ($requiredPath in @("go.mod", "go.sum", "vendor", "vendor/modules.txt")) {
    if (-not (Test-Path -LiteralPath (Join-Path $backendPath $requiredPath))) {
        throw "Backend offline dependency input is missing: $requiredPath"
    }
}

$goModule = Get-Content -LiteralPath (Join-Path $backendPath "go.mod") -Raw -Encoding UTF8
if ($goModule -notmatch '(?m)^go 1\.23\.0\s*$') {
    throw "go.mod must declare exactly Go 1.23.0."
}
if ($goModule -match '(?m)^toolchain\s+(?!go1\.23\.0\s*$)') {
    throw "go.mod toolchain must be Go 1.23.0 when present."
}

$goCommand = Get-Command $GoExecutable -ErrorAction SilentlyContinue
if ($null -eq $goCommand) {
    throw "Go 1.23.0 is not available through the requested Go executable."
}
$goVersion = (& $goCommand.Source version).Trim()
if ($goVersion -notmatch '^go version go1\.23\.0\s+') {
    throw "The requested Go executable is not exactly Go 1.23.0."
}

$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ("federated-iot-go-offline-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $temporaryRoot | Out-Null
$previousEnvironment = @{}
foreach ($name in @("GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOCACHE", "GOMODCACHE")) {
    $previousEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

try {
    $env:GOTOOLCHAIN = "local"
    $env:GOPROXY = "off"
    $env:GOSUMDB = "off"
    $env:GOCACHE = (Join-Path $temporaryRoot "build-cache")
    $codeRoot = (Split-Path -Parent $backendPath)
    Test-DeclaredFileManifest $codeRoot $OfflineInputsManifest
    Test-CanonicalVendorTree $backendPath $VendorTreeManifest
    $sbom = Get-Content -LiteralPath (Join-Path $codeRoot "deploy\\sbom\\backend-go-1.23.0.cdx.json") -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($sbom.bomFormat -ne "CycloneDX" -or $sbom.specVersion -ne "1.5" -or @($sbom.components).Count -ne 7) {
        throw "Backend dependency SBOM is not the expected CycloneDX 1.5 inventory."
    }
    if (-not [string]::IsNullOrWhiteSpace($ModuleCacheDirectory)) {
        $moduleCachePath = (Resolve-Path -LiteralPath $ModuleCacheDirectory).Path
        if ([string]::IsNullOrWhiteSpace($ModuleCacheManifest)) {
            throw "A supplied module cache requires its checksum manifest."
        }
        Test-Sha256Manifest $moduleCachePath $ModuleCacheManifest ""
        $env:GOMODCACHE = $moduleCachePath
    }

    $goEnvironment = @(& $goCommand.Source env GOTOOLCHAIN GOPROXY GOSUMDB)
    if (($goEnvironment -join "|") -ne "local|off|off") {
        throw "The Go command did not retain the required offline environment."
    }

    Push-Location $backendPath
    try {
        if (-not [string]::IsNullOrWhiteSpace($ModuleCacheDirectory)) {
            & $goCommand.Source mod verify
            if ($LASTEXITCODE -ne 0) { throw "go mod verify failed against the supplied module cache." }
        }
        & $goCommand.Source test -mod=vendor ./...
        if ($LASTEXITCODE -ne 0) { throw "Offline vendor test failed." }
        $outputBinary = Join-Path $temporaryRoot "web-api-offline-check.exe"
        & $goCommand.Source build -mod=vendor -o $outputBinary ./cmd/web-api
        if ($LASTEXITCODE -ne 0) { throw "Offline vendor build failed." }
    } finally {
        Pop-Location
    }
} finally {
    foreach ($name in $previousEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $previousEnvironment[$name], "Process")
    }
    if (Test-Path -LiteralPath $temporaryRoot) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}

Write-Output "Backend offline Go 1.23.0 supply-chain verification passed."
