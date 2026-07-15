#!/bin/sh
set -eu

: "${MAILWISP_LMTP_PORT:?MAILWISP_LMTP_PORT is required}"

case "$MAILWISP_LMTP_PORT" in
    *[!0-9]*|'')
        echo "MAILWISP_LMTP_PORT must be a numeric TCP port" >&2
        exit 64
        ;;
esac

if [ "$MAILWISP_LMTP_PORT" -lt 1 ] || [ "$MAILWISP_LMTP_PORT" -gt 65535 ]; then
    echo "MAILWISP_LMTP_PORT must be between 1 and 65535" >&2
    exit 64
fi

postconf -e "relay_transport = lmtp:[host.testcontainers.internal]:${MAILWISP_LMTP_PORT}"
postfix check

exec postfix start-fg
