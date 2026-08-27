[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$PackageDirectory
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

$packageRoot = (Resolve-Path -LiteralPath $PackageDirectory).Path
$forbiddenPath = '(^|[\\/])(secrets|credentials|datasets|artifacts|results|exports|backups|logs|source|qa|testdata|fixtures|sample-data)([\\/]|$)|(^|[\\/])\.runtime\.env$|(^|[\\/])\.env$|\.(pem|key|p12|pfx|secret)$|(^|[\\/])[^\\/]*HANDOFF[^\\/]*$|(^|[\\/])PAUSE_CHECKPOINT[^\\/]*$'
$secretPattern = '-----BEGIN (?:RSA |EC |OPENSSH |)?PRIVATE KEY-----|\bAKIA[0-9A-Z]{16}\b|\b(?:ghp_[A-Za-z0-9]{36}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{20,})\b'
$violations = [System.Collections.Generic.List[string]]::new()
foreach ($file in Get-ChildItem -LiteralPath $packageRoot -File -Recurse -Force) {
    $relative = $file.FullName.Substring($packageRoot.Length).TrimStart('\','/').Replace('\','/')
    if ($relative -match $forbiddenPath) { $violations.Add("forbidden path: $relative"); continue }
    if ($file.Length -le 4MB) {
        try {
            $content = Get-Content -LiteralPath $file.FullName -Raw -Encoding UTF8 -ErrorAction Stop
            if ($content -match $secretPattern) { $violations.Add("credential pattern: $relative") }
        } catch { }
    }
}
if ($violations.Count -gt 0) { throw "Offline package protection validation failed: $($violations -join '; ')" }
Write-Output "Offline package protection validation passed."
