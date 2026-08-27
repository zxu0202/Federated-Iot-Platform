#!/usr/bin/env sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
environment_file=${ENVIRONMENT_FILE:-"$script_dir/../.env"}
config_file=${CONFIG_FILE:-"$script_dir/../config/platform.yaml"}
compose_file=${COMPOSE_FILE:-"$script_dir/../compose.postgres.yaml"}
project_name=${COMPOSE_PROJECT_NAME:-federated-iot-platform}
build=false
bind_address=
bind_interface=

while [ "$#" -gt 0 ]; do
    case "$1" in
        --build) build=true; shift ;;
        --bind-address) bind_address=${2:?--bind-address requires an IPv4 value}; shift 2 ;;
        --bind-interface) bind_interface=${2:?--bind-interface requires an interface name}; shift 2 ;;
        *) printf '%s\n' "Unknown startup argument: $1" >&2; exit 2 ;;
    esac
done

sh "$script_dir/test-deployment-config.sh" "$environment_file" "$config_file" "$script_dir/../versions.release-freeze.yaml" "$compose_file"

get_env_value() {
    key=$1
    sed -n "s/^${key}=//p" "$environment_file" | tail -n 1 | sed 's/^"//;s/"$//'
}

is_usable_ipv4() {
    case "$1" in
        ''|0.*|127.*|169.254.*|255.*) return 1 ;;
        *.*.*.*) return 0 ;;
        *) return 1 ;;
    esac
}

first_interface_ip() {
    interface_name=$1
    ip -o -4 addr show dev "$interface_name" up scope global 2>/dev/null |
        awk '{split($4, parts, "/"); print parts[1]}' |
        while IFS= read -r address; do
            if is_usable_ipv4 "$address"; then
                printf '%s\n' "$address"
                break
            fi
        done
}

selected_address=$bind_address
if [ -z "$selected_address" ] && [ -n "$bind_interface" ]; then
    selected_address=$(first_interface_ip "$bind_interface" || true)
    if [ -z "$selected_address" ]; then
        printf '%s\n' "--bind-interface did not resolve to a usable IPv4 address." >&2
        exit 1
    fi
fi
if [ -z "$selected_address" ]; then
    selected_address=$(get_env_value PLATFORM_BIND_ADDRESS)
fi
if [ -z "$selected_address" ]; then
    explicit_interface=$(get_env_value PLATFORM_BIND_INTERFACE)
    candidate_interfaces=$(get_env_value PLATFORM_CANDIDATE_INTERFACES)
    if [ -n "$explicit_interface" ]; then
        interface_list=$explicit_interface
    elif [ -n "$candidate_interfaces" ]; then
        interface_list=$(printf '%s' "$candidate_interfaces" | tr ',' ' ')
    else
        interface_list=
    fi
    for interface_name in $interface_list; do
        selected_address=$(first_interface_ip "$interface_name" || true)
        if [ -n "$selected_address" ]; then
            break
        fi
    done
    if [ -n "$interface_list" ] && [ -z "$selected_address" ]; then
        printf '%s\n' "Configured host binding interface did not resolve to a usable IPv4 address." >&2
        exit 1
    fi
fi
if [ -z "$selected_address" ]; then
    selected_address=0.0.0.0
fi
if [ "$selected_address" != "0.0.0.0" ] && { ! is_usable_ipv4 "$selected_address" || ! ip -o -4 addr show | awk '{split($4, parts, "/"); print parts[1]}' | grep -Fxq "$selected_address"; }; then
    printf '%s\n' "PLATFORM_BIND_ADDRESS must be 0.0.0.0 or a usable IPv4 address assigned to this host." >&2
    exit 1
fi

export PLATFORM_BIND_ADDRESS="$selected_address"
docker compose --env-file "$environment_file" -f "$compose_file" --project-name "$project_name" config --quiet
if [ "$build" = true ]; then
    docker compose --env-file "$environment_file" -f "$compose_file" --project-name "$project_name" up --detach --build
else
    docker compose --env-file "$environment_file" -f "$compose_file" --project-name "$project_name" up --detach
fi

printf '%s\n' "Platform startup requested with Web/API published on $selected_address."
