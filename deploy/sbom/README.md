# SBOM Delivery Location

`backend-go-1.23.0.cdx.json` is an M1 source-derived CycloneDX dependency SBOM
for the approved Backend `go.mod`, `go.sum`, and `vendor/modules.txt` snapshot.
It is not an image SBOM and does not claim that a release image has been built.
Public Git omits the vendor tree; this SBOM describes the frozen local release
input that must be reconstructed and hash-verified before an offline build.

Place release-generated SPDX JSON or CycloneDX JSON image SBOMs here only for a
frozen release. Record each image SBOM SHA-256 in
`../versions.release-freeze.yaml` after the relevant immutable image digest is
verified. Keep those SBOMs and their SHA-256 values in the local offline package
as well. SBOM generation and validation are local-only; no automated process uploads an
SBOM to a registry, Docker Hub, Zenodo, or another external service.
