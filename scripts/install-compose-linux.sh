#!/usr/bin/env sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
lock_file="$repository_root/deploy/compose/versions.lock"

compose_version=$(awk -F= '$1 == "MAILWISP_DOCKER_COMPOSE" { print $2 }' "$lock_file")
compose_sha256=$(awk -F= '$1 == "MAILWISP_DOCKER_COMPOSE_LINUX_X86_64_SHA256" { print $2 }' "$lock_file")
case "$compose_version" in
    ''|*[!0-9.]*) echo 'invalid locked Docker Compose version' >&2; exit 1 ;;
esac
case "$compose_sha256" in
    ''|*[!0-9a-f]*) echo 'invalid locked Docker Compose SHA-256' >&2; exit 1 ;;
esac
test "${#compose_sha256}" -eq 64

plugin_root=${DOCKER_CONFIG:-"$HOME/.docker"}
plugin_directory="$plugin_root/cli-plugins"
temporary_binary=$(mktemp "${TMPDIR:-/tmp}/mailwisp-docker-compose.XXXXXX")
trap 'rm -f "$temporary_binary"' EXIT HUP INT TERM

install -d -m 0755 "$plugin_directory"
curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location --retry 5 --retry-all-errors \
    "https://github.com/docker/compose/releases/download/v${compose_version}/docker-compose-linux-x86_64" \
    --output "$temporary_binary"
printf '%s  %s\n' "$compose_sha256" "$temporary_binary" | sha256sum --check --strict
install -m 0755 "$temporary_binary" "$plugin_directory/docker-compose"
test "$(docker compose version --short)" = "$compose_version"
