#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
backend_dir=${BACKEND_DIRECTORY:-"$script_dir/../../backend"}
go_executable=${GO_EXECUTABLE:-go}
vendor_tree_manifest=${VENDOR_TREE_MANIFEST:-"$script_dir/../offline/go/backend-vendor.tree.sha256"}
offline_inputs_manifest=${OFFLINE_INPUTS_MANIFEST:-"$script_dir/../offline/go/backend-offline-inputs.sha256"}
module_cache_dir=${MODULE_CACHE_DIRECTORY:-}
module_cache_manifest=${MODULE_CACHE_MANIFEST:-}

if ! command -v "$go_executable" >/dev/null 2>&1; then
    printf '%s\n' "Go 1.23.0 is not available through the requested Go executable." >&2
    exit 1
fi
for required_path in go.mod go.sum vendor vendor/modules.txt; do
    if [ ! -e "$backend_dir/$required_path" ]; then
        printf '%s\n' "Backend offline dependency input is missing: $required_path" >&2
        exit 1
    fi
done
if ! grep -Eq '^go 1\.23\.0[[:space:]]*$' "$backend_dir/go.mod"; then
    printf '%s\n' "go.mod must declare exactly Go 1.23.0." >&2
    exit 1
fi
if grep -Eq '^toolchain[[:space:]]+' "$backend_dir/go.mod" && ! grep -Eq '^toolchain[[:space:]]+go1\.23\.0[[:space:]]*$' "$backend_dir/go.mod"; then
    printf '%s\n' "go.mod toolchain must be Go 1.23.0 when present." >&2
    exit 1
fi
if ! "$go_executable" version | grep -Eq '^go version go1\.23\.0[[:space:]]+'; then
    printf '%s\n' "The requested Go executable is not exactly Go 1.23.0." >&2
    exit 1
fi
if ! command -v sha256sum >/dev/null 2>&1; then
    printf '%s\n' "sha256sum is required for offline checksum verification." >&2
    exit 1
fi

verify_manifest() {
    root=$1
    manifest=$2
    required_prefix=$3
    if [ ! -f "$manifest" ]; then
        printf '%s\n' "Offline checksum manifest is missing." >&2
        return 1
    fi
    if ! awk -v prefix="$required_prefix" '
        {
            if ($0 !~ /^[a-f0-9]{64}  .+$/) exit 1
            path = substr($0, 67)
            if (index(path, prefix) != 1 || path ~ /(^|\/)\.\.($|\/)/ || seen[path]++) exit 1
        }
    ' "$manifest"; then
        printf '%s\n' "Offline checksum manifest has an invalid or unsafe entry." >&2
        return 1
    fi
    if [ ! -s "$manifest" ] || ! LC_ALL=C cut -d ' ' -f 3- "$manifest" | LC_ALL=C sort -c; then
        printf '%s\n' "Offline checksum manifest is empty or not bytewise sorted." >&2
        return 1
    fi
    actual_file_list=$(mktemp)
    expected_file_list=$(mktemp)
    if [ -n "$required_prefix" ]; then
        (cd "$root" && find "${required_prefix%/}" -type f -print | LC_ALL=C sort) > "$actual_file_list"
    else
        (cd "$root" && find . -type f -print | sed 's#^./##' | LC_ALL=C sort) > "$actual_file_list"
    fi
    LC_ALL=C cut -d ' ' -f 3- "$manifest" | LC_ALL=C sort > "$expected_file_list"
    if ! diff -u "$expected_file_list" "$actual_file_list"; then
        rm -f "$actual_file_list" "$expected_file_list"
        printf '%s\n' "Offline checksum manifest does not cover exactly the supplied files." >&2
        return 1
    fi
    while IFS='  ' read -r expected_hash relative_path; do
        actual_hash=$(sha256sum "$root/$relative_path" | awk '{print $1}')
        if [ "$expected_hash" != "$actual_hash" ]; then
            rm -f "$actual_file_list" "$expected_file_list"
            printf '%s\n' "Offline checksum verification failed." >&2
            return 1
        fi
    done < "$manifest"
    rm -f "$actual_file_list" "$expected_file_list"
}

verify_declared_files() {
    root=$1
    manifest=$2
    if [ ! -f "$manifest" ]; then
        printf '%s\n' "Offline input checksum manifest is missing." >&2
        return 1
    fi
    if ! awk '
        {
            if ($0 !~ /^[a-f0-9]{64}  .+$/) exit 1
            path = substr($0, 67)
            if (path ~ /(^|\/)\.\.($|\/)/ || seen[path]++) exit 1
        }
    ' "$manifest"; then
        printf '%s\n' "Offline input checksum manifest has an invalid or unsafe entry." >&2
        return 1
    fi
    while IFS= read -r line; do
        expected_hash=$(printf '%s' "$line" | cut -c 1-64)
        relative_path=$(printf '%s' "$line" | cut -c 67-)
        if [ ! -f "$root/$relative_path" ]; then
            printf '%s\n' "Offline input checksum manifest references a missing file." >&2
            return 1
        fi
        actual_hash=$(sha256sum "$root/$relative_path" | awk '{print $1}')
        if [ "$expected_hash" != "$actual_hash" ]; then
            printf '%s\n' "Offline input checksum verification failed." >&2
            return 1
        fi
    done < "$manifest"
}

verify_vendor_tree() {
    root=$1
    manifest=$2
    if [ ! -f "$manifest" ] || ! grep -Eq '^[a-f0-9]{64}  vendor/$' "$manifest" || [ "$(wc -l < "$manifest" | tr -d ' ')" -ne 1 ]; then
        printf '%s\n' "Vendor tree checksum manifest must contain exactly one canonical vendor entry." >&2
        return 1
    fi
    expected_hash=$(cut -c 1-64 "$manifest")
    actual_hash=$(
        cd "$root"
        find vendor -type f -print | while IFS= read -r relative_path; do
            sha256sum "$relative_path"
        done | LC_ALL=C sort | sha256sum | awk '{print $1}'
    )
    if [ "$expected_hash" != "$actual_hash" ]; then
        printf '%s\n' "Vendor tree checksum verification failed." >&2
        return 1
    fi
}

temporary_root=$(mktemp -d)
trap 'rm -rf "$temporary_root"' EXIT
export GOTOOLCHAIN=local
export GOPROXY=off
export GOSUMDB=off
export GOCACHE="$temporary_root/build-cache"

code_root=$(CDPATH= cd -- "$backend_dir/.." && pwd)
verify_declared_files "$code_root" "$offline_inputs_manifest"
verify_vendor_tree "$backend_dir" "$vendor_tree_manifest"
if ! grep -Fq '"bomFormat": "CycloneDX"' "$code_root/deploy/sbom/backend-go-1.23.0.cdx.json" \
    || ! grep -Fq '"specVersion": "1.5"' "$code_root/deploy/sbom/backend-go-1.23.0.cdx.json"; then
    printf '%s\n' "Backend dependency SBOM is not the expected CycloneDX 1.5 inventory." >&2
    exit 1
fi

if [ "$("$go_executable" env GOTOOLCHAIN)" != "local" ] || [ "$("$go_executable" env GOPROXY)" != "off" ] || [ "$("$go_executable" env GOSUMDB)" != "off" ]; then
    printf '%s\n' "The Go command did not retain the required offline environment." >&2
    exit 1
fi

if [ -n "$module_cache_dir" ]; then
    if [ -z "$module_cache_manifest" ]; then
        printf '%s\n' "A supplied module cache requires its checksum manifest." >&2
        exit 1
    fi
    verify_manifest "$module_cache_dir" "$module_cache_manifest" ""
    export GOMODCACHE="$module_cache_dir"
fi
cd "$backend_dir"
if [ -n "$module_cache_dir" ]; then
    "$go_executable" mod verify
fi
"$go_executable" test -mod=vendor ./...
"$go_executable" build -mod=vendor -o "$temporary_root/web-api-offline-check" ./cmd/web-api
printf '%s\n' "Backend offline Go 1.23.0 supply-chain verification passed."
