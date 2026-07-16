#!/bin/sh
set -eu

: "${MAILWISP_SMTP_HOST:?MAILWISP_SMTP_HOST is required}"
: "${MAILWISP_MAIL_DOMAIN:?MAILWISP_MAIL_DOMAIN is required}"
: "${MAILWISP_CERT_NAME:?MAILWISP_CERT_NAME is required}"

postconf -e 'compatibility_level = 3.11'
postconf -e "myhostname = ${MAILWISP_SMTP_HOST}"
postconf -e 'myorigin = $myhostname'
postconf -e 'mydestination ='
postconf -e 'inet_interfaces = all'
postconf -e 'inet_protocols = all'
postconf -e "relay_domains = ${MAILWISP_MAIL_DOMAIN}"
postconf -e 'relay_transport = lmtp:inet:app:2525'
postconf -e 'transport_maps ='
postconf -e 'alias_maps ='
postconf -e 'alias_database ='
postconf -e 'smtpd_banner = $myhostname ESMTP'
postconf -e 'disable_vrfy_command = yes'
postconf -e 'smtpd_helo_required = yes'
postconf -e 'smtpd_delay_reject = yes'
postconf -e 'smtpd_recipient_restrictions = reject_unauth_destination'
postconf -e 'smtpd_relay_restrictions = reject_unauth_destination'
postconf -e 'message_size_limit = 26214400'
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
