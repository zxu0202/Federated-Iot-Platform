Set-StrictMode -Version Latest

function Resolve-DockerExecutable {
    param(
        [Parameter(Mandatory)]
        [ValidateNotNullOrEmpty()]
        [string]$RequestedExecutable
    )

    if ([IO.Path]::IsPathRooted($RequestedExecutable)) {
        if (-not (Test-Path -LiteralPath $RequestedExecutable -PathType Leaf)) {
            throw "The requested Docker executable does not exist: $RequestedExecutable"
        }
        $resolvedExecutable = (Resolve-Path -LiteralPath $RequestedExecutable).Path
    } else {
        $command = Get-Command $RequestedExecutable -CommandType Application -ErrorAction SilentlyContinue
        if ($null -eq $command) {
            throw "Docker CLI is not visible in this process PATH. Supply -DockerExecutable with the absolute path to docker.exe."
        }
        $resolvedExecutable = $command.Source
    }

    if (-not [string]::Equals([IO.Path]::GetFileName($resolvedExecutable), "docker.exe", [StringComparison]::OrdinalIgnoreCase)) {
        throw "-DockerExecutable must resolve to docker.exe."
    }
    return $resolvedExecutable
}

function Invoke-DockerCommand {
    param(
        [Parameter(Mandatory)]
        [ValidateNotNullOrEmpty()]
        [string]$DockerExecutable,
        [Parameter(Mandatory)]
        [string[]]$Arguments
    )

    $dockerDirectory = Split-Path -Parent $DockerExecutable
    $previousPath = $env:PATH
    try {
        $env:PATH = if ([string]::IsNullOrEmpty($previousPath)) {
            $dockerDirectory
        } else {
            "$dockerDirectory$([IO.Path]::PathSeparator)$previousPath"
        }
        & $DockerExecutable @Arguments
        if ($LASTEXITCODE -ne 0) {
            throw "Docker command failed with exit code $LASTEXITCODE."
        }
    } finally {
        $env:PATH = $previousPath
    }
}
