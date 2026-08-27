# Offline Package and Manual Distribution

## Non-negotiable boundary

All source, images, SBOMs, test evidence, and offline packages remain local.
No automation may run `docker push`, an image registry upload, a Zenodo API request,
or a publication command. This runbook defines a manual operation only. The
commands in the external distribution sections must be copied and executed by
an authorised human in a separate terminal after a separate approval; project
automation must not execute them.

## Local package export

After the version lock is truly `frozen`, all images are present locally by
immutable digest, application-image SBOMs exist, and the final release manifest
has been completed, an operator may create a local package:

```powershell
$DockerExecutable = 'docker'
.\scripts\Export-OfflineReleasePackage.ps1 `
  -DockerExecutable $DockerExecutable `
  -OutputRoot .\release `
  -ReleaseId <approved-release-id> `
  -ReleaseManifestPath .\offline\package\release-manifest.json `
  -PostgresImageReference '<postgres-image-with-exact-tag>@sha256:<64-hex>' `
  -WebApiImageReference '<web-api-image-with-exact-tag>@sha256:<64-hex>' `
  -WorkerImageReference '<worker-image-with-exact-tag>@sha256:<64-hex>' `
  -SbomPath @('.\sbom\postgres.cdx.json', '.\sbom\web-api.cdx.json', '.\sbom\algorithm-worker.cdx.json') `
  -ProvenancePath @('.\release-evidence\postgres-18.6-bookworm.oci-index.json', '.\release-evidence\base-images.oci-indexes.json')
```

The exporter is intentionally local-only. It rejects an open lock, a mutable
reference, a missing local image, and an existing output directory. It creates
`docker image save` archives and a sorted SHA-256 manifest, but never contacts
an external service.

The exporter creates independent `release/dockerhub/<approved-release-id>/` and
`release/zenodo/<approved-release-id>/` directories. Validate either package
from a disconnected host before any distribution decision:

```powershell
.\release\zenodo\<approved-release-id>\validate\Validate-OfflinePackage.ps1 `
  -PackageDirectory .\release\zenodo\<approved-release-id>
```

## Manual Docker Hub distribution

This section is not an automated instruction to execute. An authorised human may
manually tag and push a validated application image after receiving a separate
distribution approval. Record the returned repository digest in the final
release manifest, then re-export and validate the local package.

```powershell
# Run only after separate distribution approval. Enter credentials interactively.
$DockerExecutable = 'docker'
$localImage = 'zx/federated-iot-platform:<approved-release-tag>'
$remoteImage = 'docker.io/<approved-namespace>/federated-iot-platform:<approved-release-tag>'
& $DockerExecutable tag $localImage $remoteImage
& $DockerExecutable push $remoteImage
& $DockerExecutable buildx imagetools inspect $remoteImage --format '{{json .Manifest}}'
```

## Manual Zenodo distribution

This section is also not an automated instruction to execute. An authorised human
must use a separately approved Zenodo token and package path. Do not put a
token in this repository, an image, a manifest, a terminal transcript, or a
chat message. Preserve the returned deposition identifier and file checksums in
the locally retained release evidence.

```powershell
# Run only after separate distribution approval. Read the token from a secret store.
$zenodoToken = '<read manually from approved secret store>'
$PackageRoot = '<release-directory>\<approved-release-id>'
$metadata = @{ metadata = @{ title = '<approved title>'; upload_type = 'software'; description = '<approved description>' } } | ConvertTo-Json -Depth 4
$deposition = Invoke-RestMethod -Method Post -Uri "https://zenodo.org/api/deposit/depositions?access_token=$zenodoToken" -ContentType 'application/json' -Body $metadata
Get-ChildItem -LiteralPath $PackageRoot -File -Recurse | ForEach-Object {
  Invoke-RestMethod -Method Put -Uri "$($deposition.links.bucket)/$($_.Name)?access_token=$zenodoToken" -InFile $_.FullName -ContentType 'application/octet-stream'
}
# Publication is a separate final manual decision.
Invoke-RestMethod -Method Post -Uri "https://zenodo.org/api/deposit/depositions/$($deposition.id)/actions/publish?access_token=$zenodoToken"
```

The final `publish` command must not be run merely because an upload succeeded.
The human operator must retain the release-lock digest, package checksum
manifest, generated DOI, and public record URL locally as the distribution
audit trail.
