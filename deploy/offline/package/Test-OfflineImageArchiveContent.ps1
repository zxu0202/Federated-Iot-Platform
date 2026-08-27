[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$PackageDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-TarExecutable {
    $tar = Get-Command tar -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $tar) {
        throw "A tar-compatible executable is required to inspect the local image archives."
    }
    return $tar.Source
}

function Invoke-TarList([string]$TarExecutable, [string]$ArchivePath) {
    $entries = & $TarExecutable -tf $ArchivePath
    if ($LASTEXITCODE -ne 0) {
        throw "Could not list archive content: $ArchivePath"
    }
    return @($entries | ForEach-Object { ([string]$_).TrimStart('.', '/').Replace('\\', '/') })
}

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
$tarExecutable = Get-TarExecutable
$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) ("federated-iot-image-archive-audit-" + [Guid]::NewGuid().ToString("N"))
$forbiddenRuntimeData = '(^|/)(testdata|fixtures?|datasets?|sample-data|qa)(/|$)|^(opt/federated-iot|app|workspace)/(tests?|testdata|fixtures?)(/|$)'
$violations = [System.Collections.Generic.List[string]]::new()

try {
    [IO.Directory]::CreateDirectory($temporaryRoot) | Out-Null
    foreach ($imageName in @("postgres", "web-api", "algorithm-worker")) {
        $archivePath = Join-Path $packageRoot "images\\$imageName.tar"
        if (-not (Test-Path -LiteralPath $archivePath -PathType Leaf)) {
            throw "Required image archive is missing: $imageName"
        }
        $extractRoot = Join-Path $temporaryRoot $imageName
        [IO.Directory]::CreateDirectory($extractRoot) | Out-Null
        & $tarExecutable -xf $archivePath -C $extractRoot
        if ($LASTEXITCODE -ne 0) {
            throw "Could not read the local image archive: $imageName"
        }
        foreach ($layerArchive in Get-ChildItem -LiteralPath $extractRoot -Filter "layer.tar" -File -Recurse) {
            foreach ($entry in Invoke-TarList $tarExecutable $layerArchive.FullName) {
                if ($entry -match $forbiddenRuntimeData) {
                    $violations.Add("$imageName image layer contains excluded runtime data: $entry")
                }
            }
        }
    }
} finally {
    if (Test-Path -LiteralPath $temporaryRoot) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}

if ($violations.Count -gt 0) {
    throw "Image archive content validation failed: $($violations -join '; ')"
}
Write-Output "Image archive content validation passed. No product test data, fixtures, datasets, or QA payloads were found."
