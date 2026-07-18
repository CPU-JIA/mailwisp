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
postconf -e "address_verify_relay_transport = lmtp:[host.testcontainers.internal]:${MAILWISP_LMTP_PORT}"
postconf -e 'address_verify_map = lmdb:$data_directory/verify_cache'
postconf -e 'address_verify_positive_refresh_time = 1s'
postconf -e 'address_verify_positive_expire_time = 2s'
postconf -e 'address_verify_negative_refresh_time = 1s'
postconf -e 'address_verify_negative_expire_time = 2s'
postconf -e 'address_verify_poll_delay = 1s'
postconf -e 'address_verify_poll_count = 3'
postconf -e 'unverified_recipient_reject_code = 550'
postconf -e 'smtpd_recipient_restrictions = reject_unauth_destination, reject_unverified_recipient'
postfix check

exec postfix start-fg
