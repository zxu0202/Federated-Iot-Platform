#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
environment_file=${1:-"$script_dir/../.env"}
config_file=${2:-"$script_dir/../config/platform.yaml"}
version_lock_file=${3:-"$script_dir/../versions.release-freeze.yaml"}
compose_file=${4:-"$script_dir/../compose.postgres.yaml"}

failures=0
fail() {
    printf '%s\n' "$1" >&2
    failures=$((failures + 1))
}

for required_file in "$environment_file" "$config_file" "$version_lock_file" "$compose_file"; do
    if [ ! -f "$required_file" ]; then
        fail "Required deployment input is missing."
    fi
done
if [ "$failures" -ne 0 ]; then
    exit 1
fi

get_env_value() {
    key=$1
    sed -n "s/^${key}=//p" "$environment_file" | tail -n 1 | sed 's/^"//;s/"$//'
}

digest_reference='^.+:[^@]+@sha256:[a-f0-9]{64}$'
for role_key in MIGRATOR_DB_USER WORKER_REPOSITORY_OWNER; do
    role_value=$(get_env_value "$role_key")
    case "$role_key:$role_value" in
        MIGRATOR_DB_USER:|MIGRATOR_DB_USER:platform_migrator|WORKER_REPOSITORY_OWNER:|WORKER_REPOSITORY_OWNER:platform_worker_repository_owner) ;;
        *) fail "$role_key is fixed by the PostgreSQL ACL contract." ;;
    esac
done
for image_key in POSTGRES_IMAGE GO_BUILDER_IMAGE NODE_BUILDER_IMAGE WEB_RUNTIME_IMAGE PYTHON_BUILDER_IMAGE PYTHON_RUNTIME_IMAGE; do
    image_value=$(get_env_value "$image_key")
    if [ -z "$image_value" ] || [ "$image_value" = "RELEASE_FREEZE_REQUIRED" ]; then
        fail "$image_key is still blocked by the release-freeze gate."
    elif ! printf '%s\n' "$image_value" | grep -Eq "$digest_reference"; then
        fail "$image_key must contain an exact tag and sha256 digest."
    fi
done
for image_key in WEB_API_IMAGE ALGORITHM_WORKER_IMAGE; do
    image_value=$(get_env_value "$image_key")
    if ! printf '%s\n' "$image_value" | grep -Eq "$digest_reference"; then
        fail "$image_key must contain an exact release tag and sha256 digest after release freeze."
    fi
done
postgres_image=$(get_env_value POSTGRES_IMAGE)
if printf '%s\n' "$postgres_image" | grep -Eq '(^|:)latest(@|$)'; then
    fail "POSTGRES_IMAGE must not use the floating latest tag."
fi

if ! grep -Eq '^[[:space:]]*status:[[:space:]]*frozen[[:space:]]*$' "$version_lock_file"; then
    fail "versions.release-freeze.yaml is not frozen; release image digests have not been verified."
fi

environment_dir=$(CDPATH= cd -- "$(dirname -- "$environment_file")" && pwd)
resolved_secret_paths=
for secret_key in POSTGRES_ADMIN_PASSWORD_SOURCE WEB_API_DB_PASSWORD_SOURCE MIGRATOR_DB_PASSWORD_SOURCE WORKER_DB_PASSWORD_SOURCE; do
    secret_value=$(get_env_value "$secret_key")
    if [ -z "$secret_value" ]; then
        fail "$secret_key must point to a local secret file."
    elif [ "${secret_value#/*}" = "$secret_value" ] && [ ! -f "$environment_dir/$secret_value" ]; then
        fail "$secret_key does not reference an available local secret file."
    elif [ "${secret_value#/*}" != "$secret_value" ] && [ ! -f "$secret_value" ]; then
        fail "$secret_key does not reference an available local secret file."
    else
        if [ "${secret_value#/*}" = "$secret_value" ]; then
            resolved_secret_path="$environment_dir/$secret_value"
        else
            resolved_secret_path="$secret_value"
        fi
        if [ -n "$resolved_secret_paths" ] && printf '%s\n' "$resolved_secret_paths" | grep -F -x -- "$resolved_secret_path" >/dev/null; then
            fail "PostgreSQL administrator, migrator, Web/API, and Worker must use four distinct local secret files."
        elif [ -n "$resolved_secret_paths" ]; then
            resolved_secret_paths=$(printf '%s\n%s' "$resolved_secret_paths" "$resolved_secret_path")
        else
            resolved_secret_paths="$resolved_secret_path"
        fi
    fi
done

backend_constraints_file="$script_dir/../../backend/config/parameter-constraints.v1.json"
constraints_source=$(get_env_value PARAMETER_CONSTRAINTS_SOURCE)
if [ ! -f "$backend_constraints_file" ]; then
    fail "Backend parameter-constraints.v1.json is required for semantic parity validation."
elif [ -z "$constraints_source" ]; then
    fail "PARAMETER_CONSTRAINTS_SOURCE must point to a local parameter constraints JSON file."
elif [ "${constraints_source#/*}" = "$constraints_source" ] && [ ! -f "$environment_dir/$constraints_source" ]; then
    fail "PARAMETER_CONSTRAINTS_SOURCE does not reference an available local parameter constraints JSON file."
elif [ "${constraints_source#/*}" != "$constraints_source" ] && [ ! -f "$constraints_source" ]; then
    fail "PARAMETER_CONSTRAINTS_SOURCE does not reference an available local parameter constraints JSON file."
else
    if [ "${constraints_source#/*}" = "$constraints_source" ]; then
        constraints_file="$environment_dir/$constraints_source"
    else
        constraints_file="$constraints_source"
    fi
    if ! command -v python3 >/dev/null 2>&1; then
        fail "python3 is required to validate parameter constraints JSON."
    elif ! python3 - "$constraints_file" "$backend_constraints_file" <<'PY'
import hashlib
import json
import re
import sys

with open(sys.argv[1], encoding="utf-8") as source:
    document = json.load(source)
with open(sys.argv[2], encoding="utf-8") as source:
    backend_document = json.load(source)
if hashlib.sha256(open(sys.argv[1], "rb").read()).digest() != hashlib.sha256(open(sys.argv[2], "rb").read()).digest():
    raise SystemExit("parameter constraints SHA-256 does not match Backend")
if not isinstance(document, dict) or document.get("contract_version") != "parameter-constraints.v1":
    raise SystemExit("constraint contract_version is invalid")
rules = document.get("paths")
if not isinstance(rules, dict) or len(rules) != 69:
    raise SystemExit("paths must contain exactly 69 named entries")
path_pattern = re.compile(r"^[a-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$")
editable_count = 0
fixed_count = 0
for path, rule in rules.items():
    if not isinstance(path, str) or not path_pattern.fullmatch(path):
        raise SystemExit("a parameter path is invalid")
    if not isinstance(rule, dict) or any(key not in rule for key in ("type", "editable", "nullable", "minimum", "maximum", "allowed_values")):
        raise SystemExit("a parameter path rule is incomplete")
    if not isinstance(rule["editable"], bool) or not isinstance(rule["nullable"], bool):
        raise SystemExit("editable and nullable must be boolean")
    if rule["editable"]:
        editable_count += 1
    else:
        fixed_count += 1
    if rule["type"] not in {"integer", "number", "boolean", "string"}:
        raise SystemExit("a parameter path type is invalid")
    for bound_name in ("minimum", "maximum"):
        bound = rule[bound_name]
        if bound is not None and (isinstance(bound, bool) or not isinstance(bound, (int, float))):
            raise SystemExit("a numeric bound is invalid")
    if rule["minimum"] is not None and rule["maximum"] is not None and rule["minimum"] > rule["maximum"]:
        raise SystemExit("minimum exceeds maximum")
    if rule["allowed_values"] is not None and not isinstance(rule["allowed_values"], list):
        raise SystemExit("allowed_values must be an array or null")
if editable_count != 67 or fixed_count != 2:
    raise SystemExit("paths must contain exactly 67 editable and 2 fixed entries")
fixed = {
    "split.agent_count": ("integer", 3),
    "global_surrogate.leave_one_out": ("boolean", True),
}
for path, (expected_type, expected_value) in fixed.items():
    rule = rules.get(path)
    if not isinstance(rule, dict) or rule.get("editable") is not False or rule.get("type") != expected_type or rule.get("allowed_values") != [expected_value]:
        raise SystemExit("a fixed S1 topology entry is invalid")
if document != backend_document:
    raise SystemExit("parameter constraints semantic content does not match Backend")
PY
    then
        fail "PARAMETER_CONSTRAINTS_SOURCE is not valid UTF-8 JSON with the expected parameter-constraints.v1 shape."
    fi
fi

if ! grep -Fq 'IOT_PARAMETER_CONSTRAINTS_FILE: /etc/federated-iot/parameter-constraints.v1.json' "$compose_file"; then
    fail "Compose must expose the absolute Web/API parameter constraints file path."
fi
if ! grep -Fq 'PARAMETER_CONSTRAINTS_SOURCE' "$compose_file" \
    || ! grep -A 1 -F 'target: /etc/federated-iot/parameter-constraints.v1.json' "$compose_file" | grep -Fq 'read_only: true'; then
    fail "Compose must mount parameter constraints read-only into Web/API."
fi

database_relative_prefix='runs/'
canonical_committed_path='runs/run_contract_fixture/committed/artifact_manifest.json'
expected_artifact_path='/var/lib/iot/runs/run_contract_fixture/committed/artifact_manifest.json'
resolved_artifact_path="/var/lib/iot/$canonical_committed_path"
if [ "${canonical_committed_path#"$database_relative_prefix"}" = "$canonical_committed_path" ] \
    || [ "$resolved_artifact_path" != "$expected_artifact_path" ]; then
    fail "Artifact namespace contract must resolve database runs/ relative paths from /var/lib/iot."
fi
web_api_section=$(sed -n '/^  web-api:/,/^  algorithm-worker:/p' "$compose_file")
worker_section=$(sed -n '/^  algorithm-worker:/,/^networks:/p' "$compose_file")
if [ -z "$web_api_section" ] || [ -z "$worker_section" ]; then
    fail "Compose must define Web/API and Algorithm Worker service sections."
else
    if ! printf '%s\n' "$web_api_section" | grep -Eq '^[[:space:]]{6}IOT_ARTIFACT_ROOT:[[:space:]]*/var/lib/iot[[:space:]]*$'; then
        fail "Web/API IOT_ARTIFACT_ROOT must be the controlled /var/lib/iot namespace root."
    fi
    if ! printf '%s\n' "$web_api_section" | grep -Eq '^[[:space:]]{6}IOT_HTTP_ADDRESS:[[:space:]]*0\.0\.0\.0:8080[[:space:]]*$'; then
        fail "Web/API must listen on 0.0.0.0:8080 inside the container network."
    fi
    if ! printf '%s\n' "$web_api_section" | grep -Fq '"${PLATFORM_BIND_ADDRESS:-0.0.0.0}:${HOST_API_PORT:-8080}:8080"'; then
        fail 'Web/API host publishing must default to 0.0.0.0:${HOST_API_PORT}:8080.'
    fi
    if printf '%s\n' "$web_api_section" | grep -Eq '^[[:space:]]{6}IOT_ARTIFACT_ROOT:[[:space:]]*/var/lib/iot/runs[[:space:]]*$'; then
        fail "Web/API IOT_ARTIFACT_ROOT must not include the database runs/ relative prefix."
    fi
    if ! printf '%s\n' "$web_api_section" | grep -Eq '^[[:space:]]*-[[:space:]]*artifacts:/var/lib/iot/runs:ro[[:space:]]*$'; then
        fail "Web/API must mount artifacts at /var/lib/iot/runs read-only."
    fi
    if ! printf '%s\n' "$web_api_section" | grep -Eq '^[[:space:]]*-[[:space:]]*datasets:/var/lib/iot/datasets[[:space:]]*$'; then
        fail "Web/API must retain the datasets mount at /var/lib/iot/datasets."
    fi
    if ! printf '%s\n' "$worker_section" | grep -Eq '^[[:space:]]*-[[:space:]]*artifacts:/var/lib/iot/runs[[:space:]]*$'; then
        fail "Algorithm Worker must share the writable artifacts volume at /var/lib/iot/runs."
    fi
fi

if ! grep -Eq '^[[:space:]]*profile:[[:space:]]*postgres[[:space:]]*$' "$config_file"; then
    fail "platform.yaml must select the PostgreSQL deployment profile."
fi
if grep -Eqi '^[[:space:]]*profile:[[:space:]]*sqlite[[:space:]]*$' "$config_file"; then
    fail "SQLite is not authorised for the first S1 closed loop."
fi
for required_key in host_binding: allowed_hosts: field_standards: zl: sd: simulation_running_slots: simulation_waiting_slots: preflight_waiting_slots: worker_pool_capacity:; do
    if ! grep -Fq "$required_key" "$config_file"; then
        fail "platform.yaml is missing a required configuration section."
    fi
done
if grep -Eqi '^[[:space:]]*validation_enabled:[[:space:]]*true[[:space:]]*$' "$config_file" \
    && grep -Eqi '^[[:space:]]*(unit_symbol|standard_reference|minimum|maximum|expected_period_ms|tolerance_ms):[[:space:]]*null[[:space:]]*$' "$config_file"; then
    fail "Field validation is enabled while a field-standard value remains null."
fi

if [ "$failures" -ne 0 ]; then
    printf '%s\n' "Deployment validation failed. No containers were started." >&2
    exit 1
fi

printf '%s\n' "Deployment configuration passed static validation."
