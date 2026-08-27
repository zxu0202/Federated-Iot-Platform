#!/usr/bin/env sh
set -eu

require_secret() {
    secret_path="$1"
    if [ ! -r "$secret_path" ]; then
        printf '%s\n' "Required PostgreSQL role secret is unavailable." >&2
        exit 1
    fi
    secret_value=$(tr -d '\r\n' < "$secret_path")
    if [ -z "$secret_value" ]; then
        printf '%s\n' "Required PostgreSQL role secret is empty." >&2
        exit 1
    fi
    printf '%s' "$secret_value"
}

: "${WEB_API_DB_USER:?WEB_API_DB_USER is required}"
: "${MIGRATOR_DB_USER:?MIGRATOR_DB_USER is required}"
: "${WORKER_REPOSITORY_OWNER:?WORKER_REPOSITORY_OWNER is required}"
: "${WORKER_DB_USER:?WORKER_DB_USER is required}"
: "${WEB_API_DB_PASSWORD_FILE:?WEB_API_DB_PASSWORD_FILE is required}"
: "${MIGRATOR_DB_PASSWORD_FILE:?MIGRATOR_DB_PASSWORD_FILE is required}"
: "${WORKER_DB_PASSWORD_FILE:?WORKER_DB_PASSWORD_FILE is required}"

if [ "$MIGRATOR_DB_USER" != "platform_migrator" ] || [ "$WORKER_REPOSITORY_OWNER" != "platform_worker_repository_owner" ]; then
    printf '%s\n' "The database ACL contract fixes the migrator and Worker Repository owner role names." >&2
    exit 1
fi

web_api_password=$(require_secret "$WEB_API_DB_PASSWORD_FILE")
migrator_password=$(require_secret "$MIGRATOR_DB_PASSWORD_FILE")
worker_password=$(require_secret "$WORKER_DB_PASSWORD_FILE")
export PLATFORM_WEB_API_DB_PASSWORD="$web_api_password"
export PLATFORM_MIGRATOR_DB_PASSWORD="$migrator_password"
export PLATFORM_WORKER_DB_PASSWORD="$worker_password"

psql --set=ON_ERROR_STOP=1 \
    --set=web_api_user="$WEB_API_DB_USER" \
    --set=migrator_user="$MIGRATOR_DB_USER" \
    --set=owner_role="$WORKER_REPOSITORY_OWNER" \
    --set=worker_user="$WORKER_DB_USER" \
    --username "$POSTGRES_USER" \
    --dbname "$POSTGRES_DB" <<'SQL'
\getenv web_api_password PLATFORM_WEB_API_DB_PASSWORD
\getenv migrator_password PLATFORM_MIGRATOR_DB_PASSWORD
\getenv worker_password PLATFORM_WORKER_DB_PASSWORD
SELECT format('CREATE ROLE %I NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', :'owner_role')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'owner_role')
\gexec
SELECT format('CREATE ROLE %I LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS', :'migrator_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'migrator_user')
\gexec
SELECT format('CREATE ROLE %I NOINHERIT LOGIN', :'web_api_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'web_api_user')
\gexec
SELECT format('CREATE ROLE %I NOINHERIT LOGIN', :'worker_user')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = :'worker_user')
\gexec

ALTER ROLE :"owner_role" NOLOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE :"migrator_user" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE :"web_api_user" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE :"worker_user" LOGIN NOINHERIT NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER ROLE :"migrator_user" PASSWORD :'migrator_password';
ALTER ROLE :"web_api_user" PASSWORD :'web_api_password';
ALTER ROLE :"worker_user" PASSWORD :'worker_password';
GRANT :"owner_role" TO :"migrator_user";
REVOKE :"owner_role" FROM :"web_api_user", :"worker_user";
REVOKE CONNECT, TEMPORARY ON DATABASE :"DBNAME" FROM PUBLIC;
GRANT CONNECT ON DATABASE :"DBNAME" TO :"migrator_user", :"web_api_user", :"worker_user";
ALTER SCHEMA public OWNER TO :"migrator_user";
REVOKE CREATE ON SCHEMA public FROM PUBLIC, :"web_api_user", :"worker_user", :"owner_role";
GRANT USAGE, CREATE ON SCHEMA public TO :"migrator_user";
GRANT USAGE ON SCHEMA public TO :"web_api_user", :"worker_user", :"owner_role";
REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC, :"worker_user";
REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC, :"worker_user";
REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC;
ALTER DEFAULT PRIVILEGES FOR ROLE :"migrator_user" IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC;
SQL

touch "${PGDATA}/.platform_roles_initialized"
unset web_api_password migrator_password worker_password PLATFORM_WEB_API_DB_PASSWORD PLATFORM_MIGRATOR_DB_PASSWORD PLATFORM_WORKER_DB_PASSWORD
