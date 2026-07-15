FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
RUN apk add --no-cache postfix=3.11.5-r0 ca-certificates
COPY --chmod=0755 deploy/compose/postfix/entrypoint.sh /usr/local/bin/mailwisp-postfix-entrypoint
EXPOSE 25
ENTRYPOINT ["/usr/local/bin/mailwisp-postfix-entrypoint"]
