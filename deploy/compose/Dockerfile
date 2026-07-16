# syntax=docker/dockerfile:1.20.0@sha256:26147acbda4f14c5add9946e2fd2ed543fc402884fd75146bd342a7f6271dc1d

FROM golang:1.26.5-alpine3.24@sha256:0178a641fbb4858c5f1b48e34bdaabe0350a330a1b1149aabd498d0699ff5fb2 AS go-build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -buildvcs=false -ldflags='-s -w' -o /out/mailwisp ./cmd/mailwisp

FROM node:22.20.0-alpine3.22@sha256:dbcedd8aeab47fbc0f4dd4bffa55b7c3c729a707875968d467aaaea42d6225af AS web-build
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm install --global npm@11.15.0 \
    && npm --version | grep -Fx '11.15.0' \
    && npm ci
COPY web/ ./
RUN npm run build

FROM alpine:3.24.1@sha256:28bd5fe8b56d1bd048e5babf5b10710ebe0bae67db86916198a6eec434943f8b AS app
COPY --from=go-build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=go-build /out/mailwisp /usr/local/bin/mailwisp
RUN mkdir -p /var/lib/mailwisp/content && chown -R 65532:65532 /var/lib/mailwisp
USER 65532:65532
WORKDIR /var/lib/mailwisp
ENTRYPOINT ["/usr/local/bin/mailwisp"]
CMD ["serve"]

FROM nginx:1.30.3-alpine@sha256:0d3b80406a13a767339fbe2f41406d6c7da727ab89cf8fae399e81f780f814d1 AS edge
COPY --from=web-build /src/web/dist/ /usr/share/nginx/html/
COPY deploy/compose/nginx/security-headers.conf /etc/nginx/snippets/mailwisp-security-headers.conf
COPY deploy/compose/nginx/default.conf.template /etc/nginx/templates/default.conf.template
