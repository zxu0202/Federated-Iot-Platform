[CmdletBinding()]
param(
    [Parameter(Mandatory)]
    [string]$ImageReference,
    [Parameter(Mandatory)]
    [string]$OutputPath,
    [string]$DockerExecutable = "docker"
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Resolve-DockerExecutable([string]$RequestedExecutable) {
    if ([IO.Path]::IsPathRooted($RequestedExecutable)) {
        if (-not (Test-Path -LiteralPath $RequestedExecutable -PathType Leaf)) {
            throw "The requested Docker executable does not exist."
        }
        return (Resolve-Path -LiteralPath $RequestedExecutable).Path
    }
    $command = Get-Command $RequestedExecutable -CommandType Application -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Docker CLI is not visible in this process PATH."
    }
    return $command.Source
}

$docker = Resolve-DockerExecutable $DockerExecutable
$inspectText = & $docker image inspect $ImageReference --format '{{json .}}'
if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($inspectText)) {
    throw "Local image inspection failed."
}
$image = $inspectText | ConvertFrom-Json
$imageId = [string]$image.Id
if ($imageId -notmatch '^sha256:[a-f0-9]{64}$') {
    throw "Local image inspection did not return a SHA-256 image ID."
}
$layers = @($image.RootFS.Layers)
if ($layers.Count -eq 0) {
    throw "Local image inspection did not return root filesystem layers."
}

$documentId = "SPDXRef-DOCUMENT"
$imageSpdxId = "SPDXRef-Image"
$packages = [System.Collections.Generic.List[object]]::new()
$relationships = [System.Collections.Generic.List[object]]::new()
$packages.Add([ordered]@{
    SPDXID = $imageSpdxId
    name = $ImageReference
    versionInfo = $imageId
    downloadLocation = "NOASSERTION"
    filesAnalyzed = $false
    checksums = @([ordered]@{ algorithm = "SHA256"; checksumValue = $imageId.Substring(7) })
    licenseConcluded = "NOASSERTION"
    licenseDeclared = "NOASSERTION"
    supplier = "NOASSERTION"
})
$relationships.Add([ordered]@{ spdxElementId = $documentId; relationshipType = "DESCRIBES"; relatedSpdxElement = $imageSpdxId })
for ($index = 0; $index -lt $layers.Count; $index++) {
    $layer = [string]$layers[$index]
    if ($layer -notmatch '^sha256:[a-f0-9]{64}$') {
        throw "A root filesystem layer is not a SHA-256 diff ID."
    }
    $layerSpdxId = "SPDXRef-Layer-$index"
    $packages.Add([ordered]@{
        SPDXID = $layerSpdxId
        name = "oci-rootfs-layer-$index"
        versionInfo = $layer
        downloadLocation = "NOASSERTION"
        filesAnalyzed = $false
        checksums = @([ordered]@{ algorithm = "SHA256"; checksumValue = $layer.Substring(7) })
        licenseConcluded = "NOASSERTION"
        licenseDeclared = "NOASSERTION"
        supplier = "NOASSERTION"
    })
    $relationships.Add([ordered]@{ spdxElementId = $imageSpdxId; relationshipType = "CONTAINS"; relatedSpdxElement = $layerSpdxId })
}

$document = [ordered]@{
    spdxVersion = "SPDX-2.3"
    dataLicense = "CC0-1.0"
    SPDXID = $documentId
    name = "local-oci-image-inventory-$($imageId.Substring(7, 12))"
    documentNamespace = "https://local.invalid/spdx/$($imageId.Substring(7))"
    creationInfo = [ordered]@{
        created = [DateTime]::UtcNow.ToString("o")
        creators = @("Tool: federated-iot-local-image-inventory-sbom/1.0")
    }
    documentDescribes = @($imageSpdxId)
    packages = @($packages)
    relationships = @($relationships)
    annotations = @([ordered]@{
        annotationDate = [DateTime]::UtcNow.ToString("o")
        annotationType = "OTHER"
        annotator = "Tool: federated-iot-local-image-inventory-sbom/1.0"
        comment = "Local OCI image inventory. It binds the inspected image ID and rootfs diff IDs without contacting a registry."
    })
}

$outputDirectory = Split-Path -Parent $OutputPath
if (-not [string]::IsNullOrWhiteSpace($outputDirectory)) {
    [IO.Directory]::CreateDirectory($outputDirectory) | Out-Null
}
[IO.File]::WriteAllText($OutputPath, (($document | ConvertTo-Json -Depth 12) + [Environment]::NewLine), [Text.UTF8Encoding]::new($false))
Write-Output "Local OCI image inventory SBOM written."
