#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
environment_file=${ENVIRONMENT_FILE:-"$script_dir/../.env"}
compose_file=${COMPOSE_FILE:-"$script_dir/../compose.postgres.yaml"}
project_name=${COMPOSE_PROJECT_NAME:-federated-iot-platform}
docker_executable=${DOCKER_EXECUTABLE:-docker}
assertion_file="$script_dir/../postgres/verify/acl-assertions.sql"

for required_file in "$environment_file" "$compose_file" "$assertion_file"; do
    if [ ! -f "$required_file" ]; then
        printf '%s\n' "Required ACL verification input is missing." >&2
        exit 1
    fi
done

get_env_value() {
    key=$1
    sed -n "s/^${key}=//p" "$environment_file" | tail -n 1 | sed 's/^"//;s/"$//'
}

database=$(get_env_value POSTGRES_DB)
administrator=$(get_env_value POSTGRES_ADMIN_USER)
web_api_user=$(get_env_value WEB_API_DB_USER)
worker_user=$(get_env_value WORKER_DB_USER)
database=${database:-federated_iot}
administrator=${administrator:-platform_admin}
web_api_user=${web_api_user:-web_api}
worker_user=${worker_user:-algorithm_worker}
postgres_container=$("$docker_executable" compose --env-file "$environment_file" -f "$compose_file" --project-name "$project_name" ps -q postgres)
if [ -z "$postgres_container" ]; then
    printf '%s\n' "The PostgreSQL Compose service is not running." >&2
    exit 1
fi

"$docker_executable" exec -i "$postgres_container" psql -X -v ON_ERROR_STOP=1 -U "$administrator" -d "$database" < "$assertion_file"

service_credential_query() {
    secret_path=$1
    database_user=$2
    query=$3
    "$docker_executable" exec -i "$postgres_container" sh -ec '
set -eu
password=$(tr -d "\r\n" < "$1")
export PGPASSWORD="$password"
exec psql -h 127.0.0.1 -X -v ON_ERROR_STOP=1 -U "$2" -d "$3" -c "$4"
' acl-service-credential-check "$secret_path" "$database_user" "$database" "$query"
}

set +e
direct_select_output=$(service_credential_query /run/secrets/worker_db_password "$worker_user" "SELECT 1 FROM worker_jobs LIMIT 1;" 2>&1)
direct_select_exit=$?
set -e
if [ "$direct_select_exit" -eq 0 ] || ! printf '%s\n' "$direct_select_output" | grep -q "permission denied"; then
    printf '%s\n' "algorithm_worker direct SELECT denial was not observed." >&2
    exit 1
fi

set +e
worker_recovery_output=$(service_credential_query /run/secrets/worker_db_password "$worker_user" "SELECT worker_recover_expired_leases();" 2>&1)
worker_recovery_exit=$?
set -e
if [ "$worker_recovery_exit" -eq 0 ] || ! printf '%s\n' "$worker_recovery_output" | grep -q "permission denied"; then
    printf '%s\n' "algorithm_worker recovery-function execution denial was not observed." >&2
    exit 1
fi

service_credential_query /run/secrets/web_api_db_password "$web_api_user" "BEGIN; SELECT worker_recover_expired_leases() AS web_api_execution_proof; ROLLBACK;"
printf '%s\n' "PostgreSQL ACL gate passed: Worker direct SELECT and recovery execution were denied; Web/API recovery execution was rolled back."
