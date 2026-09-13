# syntax=docker/dockerfile:1.7

FROM node:26-alpine AS web-builder
WORKDIR /src/web
COPY web/package*.json ./
RUN npm ci
COPY web ./
COPY internal/httpx/web_dist ../internal/httpx/web_dist
RUN npm run build

FROM golang:1.26-alpine AS go-builder
ENV GOPRIVATE=github.com/Serialeo/* \
    GONOSUMDB=github.com/Serialeo/*
RUN apk add --no-cache git ca-certificates
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=secret,id=github_token,required=true \
    set -eu; \
    private_home="$(mktemp -d)"; \
    trap 'rm -rf "$private_home"' EXIT; \
    token="$(cat /run/secrets/github_token)"; \
    test -n "$token"; \
    printf 'machine github.com login x-access-token password %s\n' "$token" > "$private_home/.netrc"; \
    chmod 0600 "$private_home/.netrc"; \
    HOME="$private_home" go mod download
COPY . .
COPY --from=web-builder /src/internal/httpx/web_dist ./internal/httpx/web_dist
RUN go build -o /out/nexusdock ./cmd/nexusdock

FROM alpine:3.20
RUN apk add --no-cache git ca-certificates \
    && addgroup -S -g 10001 nexus \
    && adduser -S -D -H -u 10001 -G nexus nexus \
    && mkdir -p /var/lib/nexus /recall \
    && chown -R 10001:10001 /var/lib/nexus /recall
WORKDIR /app
COPY --from=go-builder /out/nexusdock /usr/local/bin/nexusdock
ENV NEXUS_HOST=0.0.0.0 \
    NEXUS_PORT=18777 \
    NEXUS_DATA_DIR=/var/lib/nexus \
    RECALL_REPO_DIR=/recall \
    HOME=/tmp
EXPOSE 18777
VOLUME ["/var/lib/nexus", "/recall"]
USER 10001:10001
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s --retries=3 CMD wget -q -T 2 -O /dev/null http://127.0.0.1:18777/health || exit 1
ENTRYPOINT ["nexusdock"]
