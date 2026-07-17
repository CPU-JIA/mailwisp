#!/usr/bin/env sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
lock_file="$repository_root/deploy/compose/versions.lock"

buildx_version=$(awk -F= '$1 == "MAILWISP_BUILDX" { print $2 }' "$lock_file")
buildx_sha256=$(awk -F= '$1 == "MAILWISP_BUILDX_LINUX_AMD64_SHA256" { print $2 }' "$lock_file")
case "$buildx_version" in
    ''|*[!0-9.]*) echo 'invalid locked Docker Buildx version' >&2; exit 1 ;;
esac
case "$buildx_sha256" in
    ''|*[!0-9a-f]*) echo 'invalid locked Docker Buildx SHA-256' >&2; exit 1 ;;
esac
test "${#buildx_sha256}" -eq 64

plugin_root=${DOCKER_CONFIG:-"$HOME/.docker"}
plugin_directory="$plugin_root/cli-plugins"
temporary_binary=$(mktemp "${TMPDIR:-/tmp}/mailwisp-docker-buildx.XXXXXX")
trap 'rm -f "$temporary_binary"' EXIT HUP INT TERM

install -d -m 0755 "$plugin_directory"
curl --proto '=https' --tlsv1.2 --fail --silent --show-error --location --retry 5 --retry-all-errors \
    "https://github.com/docker/buildx/releases/download/v${buildx_version}/buildx-v${buildx_version}.linux-amd64" \
    --output "$temporary_binary"
printf '%s  %s\n' "$buildx_sha256" "$temporary_binary" | sha256sum --check --strict
install -m 0755 "$temporary_binary" "$plugin_directory/docker-buildx"
docker buildx version | grep -Eq "^github.com/docker/buildx v${buildx_version} "
