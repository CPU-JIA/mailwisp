#!/bin/sh
set -eu

: "${MAILWISP_SMTP_HOST:?MAILWISP_SMTP_HOST is required}"
: "${MAILWISP_PUBLIC_DOMAINS:?MAILWISP_PUBLIC_DOMAINS is required}"
: "${MAILWISP_LMTP_MAX_MESSAGE_BYTES:?MAILWISP_LMTP_MAX_MESSAGE_BYTES is required}"
: "${MAILWISP_CERT_NAME:?MAILWISP_CERT_NAME is required}"

case "$MAILWISP_LMTP_MAX_MESSAGE_BYTES" in
    *[!0-9]*|'')
        echo "MAILWISP_LMTP_MAX_MESSAGE_BYTES must be a positive integer" >&2
        exit 64
        ;;
esac
if [ "$MAILWISP_LMTP_MAX_MESSAGE_BYTES" -lt 1 ]; then
    echo "MAILWISP_LMTP_MAX_MESSAGE_BYTES must be a positive integer" >&2
    exit 64
fi

postconf -e 'compatibility_level = 3.11'
postconf -e "myhostname = ${MAILWISP_SMTP_HOST}"
postconf -e 'myorigin = $myhostname'
postconf -e 'mydestination ='
postconf -e 'inet_interfaces = all'
postconf -e 'inet_protocols = all'
postconf -e "relay_domains = ${MAILWISP_PUBLIC_DOMAINS}"
postconf -e 'relay_transport = lmtp:inet:app:2525'
postconf -e 'address_verify_relay_transport = lmtp:inet:app:2525'
postconf -e 'address_verify_map = lmdb:$data_directory/verify_cache'
postconf -e 'address_verify_positive_refresh_time = 1s'
postconf -e 'address_verify_positive_expire_time = 2s'
postconf -e 'address_verify_negative_refresh_time = 1s'
postconf -e 'address_verify_negative_expire_time = 2s'
postconf -e 'address_verify_poll_delay = 1s'
postconf -e 'address_verify_poll_count = 3'
postconf -e 'unverified_recipient_reject_code = 550'
postconf -e 'transport_maps ='
postconf -e 'alias_maps ='
postconf -e 'alias_database ='
postconf -e 'smtpd_banner = $myhostname ESMTP'
postconf -e 'disable_vrfy_command = yes'
postconf -e 'smtpd_helo_required = yes'
postconf -e 'smtpd_delay_reject = yes'
postconf -e 'smtpd_recipient_restrictions = reject_unauth_destination, reject_unverified_recipient'
postconf -e 'smtpd_relay_restrictions = reject_unauth_destination'
postconf -e "message_size_limit = ${MAILWISP_LMTP_MAX_MESSAGE_BYTES}"
postconf -e 'smtpd_client_connection_count_limit = 20'
postconf -e 'smtpd_client_connection_rate_limit = 30'
postconf -e 'smtpd_client_message_rate_limit = 60'
postconf -e 'anvil_rate_time_unit = 60s'
postconf -e 'smtpd_tls_security_level = may'
postconf -e "smtpd_tls_cert_file = /etc/letsencrypt/live/${MAILWISP_CERT_NAME}/fullchain.pem"
postconf -e "smtpd_tls_key_file = /etc/letsencrypt/live/${MAILWISP_CERT_NAME}/privkey.pem"
postconf -e 'smtpd_tls_protocols = >=TLSv1.2'
postconf -e 'smtpd_tls_mandatory_protocols = >=TLSv1.2'
postconf -e 'smtpd_tls_received_header = yes'
postconf -e 'smtp_tls_security_level = may'
postconf -e 'maillog_file = /dev/stdout'

postfix check
exec postfix start-fg
