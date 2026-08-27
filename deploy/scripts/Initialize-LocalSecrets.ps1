[CmdletBinding()]
param(
    [string]$OutputDirectory = (Join-Path $PSScriptRoot "..\secrets")
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function New-RandomSecret {
    $bytes = [byte[]]::new(32)
    $generator = [Security.Cryptography.RandomNumberGenerator]::Create()
    try {
        $generator.GetBytes($bytes)
    } finally {
        $generator.Dispose()
    }
    return [Convert]::ToBase64String($bytes).TrimEnd('=').Replace('+', '-').Replace('/', '_')
}

$outputPath = [IO.Path]::GetFullPath($OutputDirectory)
[IO.Directory]::CreateDirectory($outputPath) | Out-Null
$secretFiles = @(
    "postgres_admin_password.txt",
    "web_api_db_password.txt",
    "platform_migrator_db_password.txt",
    "algorithm_worker_db_password.txt"
)
$createdCount = 0

foreach ($name in $secretFiles) {
    $path = Join-Path $outputPath $name
    if (Test-Path -LiteralPath $path) {
        continue
    }
    $stream = $null
    $writer = $null
    try {
        $stream = [IO.File]::Open($path, [IO.FileMode]::CreateNew, [IO.FileAccess]::Write, [IO.FileShare]::None)
        $writer = [IO.StreamWriter]::new($stream, [Text.UTF8Encoding]::new($false))
        $writer.Write((New-RandomSecret))
    } catch [IO.IOException] {
        throw "Refusing to overwrite an existing local secret: $path"
    } finally {
        if ($null -ne $writer) { $writer.Dispose() }
        elseif ($null -ne $stream) { $stream.Dispose() }
    }
    $createdCount++
}

if ($createdCount -eq 0) {
    Write-Output "All four required local secret files already exist; no files were changed."
} else {
    Write-Output "Created $createdCount missing local secret file(s) under: $outputPath"
}
Write-Output "Existing secret files were preserved and secret values were not printed. The directory is excluded from Git."
