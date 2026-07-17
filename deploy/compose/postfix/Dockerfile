FROM scratch AS postfix-packages
ADD --checksum=sha256:abe8a3badeb99bd147a0c5cba70364e8774e6ae05c625a86c01f11532d9721c8 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/ca-certificates-20260611-r0.apk /ca-certificates-20260611-r0.apk
ADD --checksum=sha256:3278ccba30b241168ca4ab93c919be805411b918fdc062f06d3684d12dc33ae3 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/gdbm-1.26-r0.apk /gdbm-1.26-r0.apk
ADD --checksum=sha256:7fc4d3abd06b6e3301c82e9f872e4555d4a41e2abc28087f4286aba01e5dd5a7 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/icu-data-en-78.1-r0.apk /icu-data-en-78.1-r0.apk
ADD --checksum=sha256:ef9a3035f23886cf0da5869902c736a775cf3db151ae1ab7f1406e2a0f84bd64 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/icu-libs-78.1-r0.apk /icu-libs-78.1-r0.apk
ADD --checksum=sha256:393dcd32629f06d7d85409c272d142d0c082772d10b87ef55ee82f47de3be637 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libgcc-15.2.0-r5.apk /libgcc-15.2.0-r5.apk
ADD --checksum=sha256:ae838d4be2c0eba0d8cdc4fe5029fcc2cb187ec5814366b9bb954e41b8dcd1d6 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libsasl-2.1.28-r9.apk /libsasl-2.1.28-r9.apk
ADD --checksum=sha256:14c987b556f5385a5db18376e788c75f37d85321b8dc1920d926ea7daac1d6f6 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libstdc++-15.2.0-r5.apk /libstdc++-15.2.0-r5.apk
ADD --checksum=sha256:90aba5a62cd665329ccc6521f051351b4dd6486fda6f360d02fc7eac0320027a https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/lmdb-0.9.35-r0.apk /lmdb-0.9.35-r0.apk
ADD --checksum=sha256:0a58f9885fb0a9ec2dd863c0d04e88a43fb8f2fdf964f6c6bc452951109d64dd https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/postfix-3.11.5-r0.apk /postfix-3.11.5-r0.apk

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b
ARG MAILWISP_VERSION=dev
ARG MAILWISP_COMMIT=unknown
ARG MAILWISP_BUILD_DATE=unknown
LABEL org.opencontainers.image.title="MailWisp Postfix" \
      org.opencontainers.image.description="Durable SMTP ingress for MailWisp" \
      org.opencontainers.image.source="https://github.com/CPU-JIA/mailwisp" \
      org.opencontainers.image.version="$MAILWISP_VERSION" \
      org.opencontainers.image.revision="$MAILWISP_COMMIT" \
      org.opencontainers.image.created="$MAILWISP_BUILD_DATE"
COPY --from=postfix-packages / /tmp/mailwisp-apks/
RUN apk --no-network --repositories-file /dev/null add /tmp/mailwisp-apks/*.apk \
    && rm -rf /tmp/mailwisp-apks /var/log/apk.log
COPY --chmod=0755 deploy/compose/postfix/entrypoint.sh /usr/local/bin/mailwisp-postfix-entrypoint
EXPOSE 25
ENTRYPOINT ["/usr/local/bin/mailwisp-postfix-entrypoint"]
