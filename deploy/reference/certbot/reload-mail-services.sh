#!/bin/sh
set -eu

if [ -z "${RENEWED_LINEAGE:-}" ]; then
    echo "RENEWED_LINEAGE is required" >&2
    exit 1
fi

install -d -m 0750 -o root -g postfix /etc/postfix/tls
install -m 0644 -o root -g postfix "$RENEWED_LINEAGE/fullchain.pem" /etc/postfix/tls/mailwisp-fullchain.pem
install -m 0640 -o root -g postfix "$RENEWED_LINEAGE/privkey.pem" /etc/postfix/tls/mailwisp-privkey.pem

nginx -t
postfix check
systemctl reload nginx
systemctl reload postfix
