#!/usr/bin/env sh
set -eu

if [ "$#" -ne 1 ]; then
    printf '%s\n' "Usage: validate-offline-package.sh <package-directory>" >&2
    exit 2
fi

package_dir=$(CDPATH= cd -- "$1" && pwd)
for required in \
    README.md \
    images/postgres.tar \
    images/web-api.tar \
    images/algorithm-worker.tar \
    manifests/release-manifest.json \
    manifests/versions.release-freeze.yaml \
    manifests/inventory.json \
    runtime/compose.postgres.yaml \
    runtime/.env.example \
    runtime/config/platform.example.yaml \
    runtime/config/parameter-constraints.v1.json \
    runtime/postgres/init/010-create-service-roles.sh \
    runtime/scripts/Initialize-LocalSecrets.ps1 \
    runtime/scripts/Test-DockerDesktopReadiness.ps1 \
    runtime/scripts/DockerCli.ps1 \
    runtime/scripts/Start-OfflinePackage.ps1 \
    runtime/scripts/Stop-OfflinePackage.ps1 \
    runtime/scripts/Test-OfflineRuntimeConfig.ps1 \
    runtime/runbooks/offline-package-operator-manual.md \
    validate/Validate-OfflinePackage.ps1 \
    validate/validate-offline-package.sh \
    validate/Test-OfflineRestorePreflight.ps1 \
    validate/test-offline-restore-preflight.sh \
    validate/Test-OfflinePackageProtection.ps1 \
    validate/Test-OfflinePackageSmoke.ps1 \
    checksums/sha256sum.txt; do
    if [ ! -f "$package_dir/$required" ]; then
        printf '%s\n' "Required package file is missing: $required" >&2
        exit 1
    fi
done

if ! grep -Eq '^[[:space:]]*status:[[:space:]]*frozen[[:space:]]*$' "$package_dir/manifests/versions.release-freeze.yaml"; then
    printf '%s\n' "Package release lock is not frozen." >&2
    exit 1
fi

if command -v sha256sum >/dev/null 2>&1; then
    (cd "$package_dir" && sha256sum -c checksums/sha256sum.txt)
elif command -v shasum >/dev/null 2>&1; then
    while IFS='  ' read -r expected relative_path; do
        actual=$(shasum -a 256 "$package_dir/$relative_path" | awk '{print $1}')
        [ "$actual" = "$expected" ] || { printf '%s\n' "Checksum mismatch: $relative_path" >&2; exit 1; }
    done < "$package_dir/checksums/sha256sum.txt"
else
    printf '%s\n' "sha256sum or shasum is required to validate this package." >&2
    exit 1
fi

printf '%s\n' "Offline package validation passed. Image loading and runtime deployment remain separate operator actions."
