#!/usr/bin/env sh
set -eu

if [ "$#" -ne 1 ]; then
    printf '%s\n' "Usage: test-offline-restore-preflight.sh <package-directory>" >&2
    exit 2
fi

package_dir=$(CDPATH= cd -- "$1" && pwd)
script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
sh "$script_dir/validate-offline-package.sh" "$package_dir"

for required in \
    runtime/runbooks/backup-recovery.md \
    runtime/compose.postgres.yaml \
    runtime/postgres/init/010-create-service-roles.sh \
    images/postgres.tar \
    images/web-api.tar \
    images/algorithm-worker.tar; do
    if [ ! -f "$package_dir/$required" ]; then
        printf '%s\n' "Restore preflight asset is missing: $required" >&2
        exit 1
    fi
done

printf '%s\n' "Offline restore preflight passed. Restore only into fresh isolated volumes using runtime/runbooks/backup-recovery.md."
