#!/bin/sh
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
lock_file="$script_dir/versions.lock"

if ! command -v docker >/dev/null 2>&1; then
    echo "Docker Engine is required" >&2
    exit 69
fi
if [ ! -r "$lock_file" ]; then
    echo "versions.lock is not readable: $lock_file" >&2
    exit 66
fi

locked_compose=$(sed -n 's/^MAILWISP_DOCKER_COMPOSE=//p' "$lock_file")
case "$locked_compose" in
    *[!0-9.]*|'')
        echo "versions.lock contains an invalid Docker Compose version" >&2
        exit 65
        ;;
esac
if [ "$(grep -c '^MAILWISP_DOCKER_COMPOSE=' "$lock_file")" -ne 1 ]; then
    echo "versions.lock must contain exactly one Docker Compose version" >&2
    exit 65
fi

actual_compose=$(docker compose version --short)
if [ "$actual_compose" != "$locked_compose" ]; then
    echo "Docker Compose $actual_compose does not match locked version $locked_compose" >&2
    exit 78
fi

engine_platform=$(docker info --format '{{.OSType}}/{{.Architecture}}')
if [ "$engine_platform" != 'linux/x86_64' ] && [ "$engine_platform" != 'linux/amd64' ]; then
    echo "MailWisp Reference Deployment requires a Linux amd64 Docker Engine; actual: $engine_platform" >&2
    exit 78
fi

echo "MailWisp Compose preflight passed: compose=$actual_compose engine=$engine_platform"
