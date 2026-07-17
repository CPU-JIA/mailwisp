# syntax=docker/dockerfile:1.20.0@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d

FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS go-build
ARG MAILWISP_VERSION=dev
ARG MAILWISP_COMMIT=unknown
ARG MAILWISP_BUILD_DATE=unknown
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN printf '%s\n' "$MAILWISP_VERSION" | grep -Eq '^(dev|(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?)$' \
    && { [ "$MAILWISP_VERSION" = dev ] || ! printf '%s\n' "$MAILWISP_VERSION" | grep -Eq -- '(-(0[0-9]+)|\.(0[0-9]+))(\.|$)'; } \
    && { [ "$MAILWISP_COMMIT" = unknown ] || { [ "${#MAILWISP_COMMIT}" -eq 40 ] && printf '%s\n' "$MAILWISP_COMMIT" | grep -Eq '^[0-9a-f]+$'; }; } \
    && printf '%s\n' "$MAILWISP_BUILD_DATE" | grep -Eq '^(unknown|[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z)$' \
    && CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X=mailwisp/internal/buildinfo.version=${MAILWISP_VERSION} -X=mailwisp/internal/buildinfo.commit=${MAILWISP_COMMIT} -X=mailwisp/internal/buildinfo.buildDate=${MAILWISP_BUILD_DATE}" \
      -o /out/mailwisp ./cmd/mailwisp

FROM node:22.20.0-alpine3.22@sha256:dbcedd8aeab47fbc0f4dd4bffa55b7c3c729a707875968d467aaaea42d6225af AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm install --global npm@11.15.0 \
    && npm --version | grep -Fx '11.15.0' \
    && npm ci
COPY web/ ./
RUN npm run build

FROM scratch AS maintenance-packages
ADD --checksum=sha256:cf8caa8a88bc4ce9d9e395567fefaf9ef7fbd55c50ae6155b35c1b58f3755023 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libncursesw-6.6_p20260516-r0.apk /libncursesw-6.6_p20260516-r0.apk
ADD --checksum=sha256:145d0d57ce40baaf2d7191e66dc18b8872822642939a0264fd4fc1c73d6599f1 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/libpq-18.4-r0.apk /libpq-18.4-r0.apk
ADD --checksum=sha256:3ad4912ab8ecf5f6236fdb751f88243006b3a8f152684e455b781e16284dd298 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/lz4-libs-1.10.0-r1.apk /lz4-libs-1.10.0-r1.apk
ADD --checksum=sha256:ce83be6d0bd10584a53c3a5868cc1595ce5f0f8e0b621f8f7f70b6144e521fc9 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/ncurses-terminfo-base-6.6_p20260516-r0.apk /ncurses-terminfo-base-6.6_p20260516-r0.apk
ADD --checksum=sha256:749d8a88b56c84b415372655435600c749345113e21ce8fe9964f7e22345cbff https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/postgresql-common-1.3-r0.apk /postgresql-common-1.3-r0.apk
ADD --checksum=sha256:d7edf2ff2d70dcb63a88d7b95b08134c4de644a4f5467807bed2ba70327f6011 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/postgresql18-client-18.4-r0.apk /postgresql18-client-18.4-r0.apk
ADD --checksum=sha256:766911ecb986a6c5cf0841a6b556cd1e9dbbf22b3559726de32416486cd15c81 https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/readline-8.3.3-r1.apk /readline-8.3.3-r1.apk
ADD --checksum=sha256:23c6065b0049b2406441564bcf0032515a43f78e80d76fcb85535a3803ef5d4e https://dl-cdn.alpinelinux.org/alpine/v3.24/main/x86_64/zstd-libs-1.5.7-r2.apk /zstd-libs-1.5.7-r2.apk

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS app
ARG MAILWISP_VERSION=dev
ARG MAILWISP_COMMIT=unknown
ARG MAILWISP_BUILD_DATE=unknown
LABEL org.opencontainers.image.title="MailWisp" \
      org.opencontainers.image.description="Production-grade self-hosted temporary email service" \
      org.opencontainers.image.source="https://github.com/CPU-JIA/mailwisp" \
      org.opencontainers.image.version="$MAILWISP_VERSION" \
      org.opencontainers.image.revision="$MAILWISP_COMMIT" \
      org.opencontainers.image.created="$MAILWISP_BUILD_DATE"
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/mailwisp /usr/local/bin/mailwisp
RUN mkdir -p /var/lib/mailwisp/content && chown -R 65532:65532 /var/lib/mailwisp
USER 65532:65532
WORKDIR /var/lib/mailwisp
ENTRYPOINT ["/usr/local/bin/mailwisp"]
CMD ["serve"]

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS maintenance
ARG MAILWISP_VERSION=dev
ARG MAILWISP_COMMIT=unknown
ARG MAILWISP_BUILD_DATE=unknown
LABEL org.opencontainers.image.title="MailWisp Maintenance" \
      org.opencontainers.image.description="Backup and recovery tooling for MailWisp" \
      org.opencontainers.image.source="https://github.com/CPU-JIA/mailwisp" \
      org.opencontainers.image.version="$MAILWISP_VERSION" \
      org.opencontainers.image.revision="$MAILWISP_COMMIT" \
      org.opencontainers.image.created="$MAILWISP_BUILD_DATE"
COPY --from=maintenance-packages / /tmp/mailwisp-apks/
RUN apk --no-network --repositories-file /dev/null add /tmp/mailwisp-apks/*.apk \
    && rm -rf /tmp/mailwisp-apks /var/log/apk.log
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/mailwisp /usr/local/bin/mailwisp
RUN mkdir -p /var/lib/mailwisp/content /backups \
    && chown -R 65532:65532 /var/lib/mailwisp /backups
USER 65532:65532
WORKDIR /var/lib/mailwisp
ENTRYPOINT ["/usr/local/bin/mailwisp"]
CMD ["maintenance-command-required"]

FROM nginx:1.30.4-alpine@sha256:59d10bca5c674965ef4ff884715000dd60ef5567c36663523f108eec8e4105d4 AS edge
ARG MAILWISP_VERSION=dev
ARG MAILWISP_COMMIT=unknown
ARG MAILWISP_BUILD_DATE=unknown
LABEL org.opencontainers.image.title="MailWisp Edge" \
      org.opencontainers.image.description="HTTPS edge and web console for MailWisp" \
      org.opencontainers.image.source="https://github.com/CPU-JIA/mailwisp" \
      org.opencontainers.image.version="$MAILWISP_VERSION" \
      org.opencontainers.image.revision="$MAILWISP_COMMIT" \
      org.opencontainers.image.created="$MAILWISP_BUILD_DATE"
COPY --from=web-build /src/web/dist/ /usr/share/nginx/html/
COPY deploy/compose/nginx/security-headers.conf /etc/nginx/snippets/mailwisp-security-headers.conf
COPY deploy/compose/nginx/default.conf.template /etc/nginx/templates/default.conf.template
