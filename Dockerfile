# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=24
ARG GO_VERSION=1.26

# 1. Build the Vue SPA
FROM node:${NODE_VERSION}-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY web/ ./
RUN npm run build

# 2. Build the Go binary (pure Go — modernc.org/sqlite, CGO disabled)
FROM golang:${GO_VERSION}-alpine AS go-builder
WORKDIR /src
COPY go.mod go.sum* ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download
COPY . .
COPY --from=web-builder /web/dist ./web/dist
ENV CGO_ENABLED=0 GOOS=linux
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    go build -trimpath -ldflags="-s -w" -o /out/recodarr ./cmd/recodarr

# 3. Runtime — Debian bookworm-slim with HandBrakeCLI + (on amd64) Intel/AMD VAAPI/QSV libs.
#    NVENC works at runtime via nvidia-container-toolkit (no extra image deps needed).
#    intel-media-va-driver is x86-only; arm64 skips it.
FROM debian:bookworm-slim
ARG TARGETARCH
ENV DEBIAN_FRONTEND=noninteractive \
    RECODARR_DATA_DIR=/data \
    RECODARR_ADDR=:8080

RUN echo "deb http://deb.debian.org/debian bookworm main contrib non-free non-free-firmware" > /etc/apt/sources.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata \
        handbrake-cli \
        libva-drm2 libva2 vainfo \
    && if [ "$TARGETARCH" = "amd64" ]; then \
         apt-get install -y --no-install-recommends intel-media-va-driver; \
       fi \
    && rm -rf /var/lib/apt/lists/*

RUN groupadd -r recodarr && useradd -r -g recodarr -u 10001 -d /data -s /usr/sbin/nologin recodarr \
    && mkdir -p /data /media \
    && chown -R recodarr:recodarr /data

COPY --from=go-builder /out/recodarr /usr/local/bin/recodarr

EXPOSE 8080
VOLUME ["/data"]
USER recodarr
ENTRYPOINT ["/usr/local/bin/recodarr"]
