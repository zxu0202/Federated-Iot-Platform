#!/usr/bin/env sh
set -eu

if [ "$#" -ne 1 ]; then
    printf '%s\n' "Usage: test-postgres-recovery-prerequisites.sh <base-url>" >&2
    exit 2
fi

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
environment_file=${ENVIRONMENT_FILE:-"$script_dir/../.env"}
config_file=${CONFIG_FILE:-"$script_dir/../config/platform.yaml"}
compose_file=${COMPOSE_FILE:-"$script_dir/../compose.postgres.yaml"}
project_name=${COMPOSE_PROJECT_NAME:-federated-iot-platform}
base_url=${1%/}

sh "$script_dir/test-deployment-config.sh" "$environment_file" "$config_file"

running_services=$(docker compose --env-file "$environment_file" -f "$compose_file" --project-name "$project_name" ps --status running --services)
for service in postgres web-api algorithm-worker; do
    if ! printf '%s\n' "$running_services" | grep -Fxq "$service"; then
        printf '%s\n' "Recovery prerequisite failed: required service is not running." >&2
        exit 1
    fi
done

if command -v curl >/dev/null 2>&1; then
    readiness_json=$(curl --fail --silent --show-error --max-time 10 "$base_url/api/v1/health/ready")
elif command -v wget >/dev/null 2>&1; then
    readiness_json=$(wget -qO- --timeout=10 "$base_url/api/v1/health/ready")
else
    printf '%s\n' "Recovery prerequisite failed: curl or wget is required for the readiness probe." >&2
    exit 1
fi

for required_fragment in '"status":"ready"' '"database_profile":"postgres"' '"database":"ok"' '"schema":"ok"' '"dataset_store":"ok"' '"artifact_store":"ok"' '"reference_profile":"ok"' '"network_binding":"ok"' '"worker":"ok"'; do
    if ! printf '%s' "$readiness_json" | tr -d '[:space:]' | grep -Fq "$required_fragment"; then
        printf '%s\n' "Recovery prerequisite failed: a required readiness check is not ok." >&2
        exit 1
    fi
done

printf '%s\n' "PostgreSQL backup and recovery prerequisites passed. Run the isolated fresh-volume recovery drill described in runbooks/backup-recovery.md."
