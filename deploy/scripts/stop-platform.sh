#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
environment_file=${ENVIRONMENT_FILE:-"$script_dir/../.env"}
compose_file=${COMPOSE_FILE:-"$script_dir/../compose.postgres.yaml"}
project_name=${COMPOSE_PROJECT_NAME:-federated-iot-platform}

docker compose --env-file "$environment_file" -f "$compose_file" --project-name "$project_name" stop --timeout 30
printf '%s\n' "Platform stop completed. Persistent volumes were retained."
