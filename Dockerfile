# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=24
ARG GO_VERSION=1.26
ARG HANDBRAKE_VERSION=1.11.1

FROM node:${NODE_VERSION}-alpine AS web-builder
WORKDIR /web
COPY web/package.json web/package-lock.json* ./
RUN --mount=type=cache,target=/root/.npm \
    if [ -f package-lock.json ]; then npm ci; else npm install; fi
COPY web/ ./
RUN npm run build

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

FROM debian:trixie-slim AS handbrake-builder
ARG HANDBRAKE_VERSION
ENV DEBIAN_FRONTEND=noninteractive
RUN echo "deb http://deb.debian.org/debian trixie main contrib non-free non-free-firmware" > /etc/apt/sources.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        autoconf automake build-essential ca-certificates clang cmake git \
        libass-dev libbz2-dev libfontconfig1-dev libfreetype6-dev libfribidi-dev \
        libharfbuzz-dev libjansson-dev liblzma-dev libmp3lame-dev libnuma-dev \
        libogg-dev libopus-dev libsamplerate-dev libspeex-dev libtheora-dev \
        libtool libtool-bin libturbojpeg0-dev libvorbis-dev libx264-dev \
        libxml2-dev libvpx-dev m4 make meson nasm ninja-build patch pkg-config \
        python3 tar zlib1g-dev xz-utils \
        libdrm-dev libva-dev libvpl-dev \
    && rm -rf /var/lib/apt/lists/*

RUN git clone --depth=1 -b ${HANDBRAKE_VERSION} \
        https://github.com/HandBrake/HandBrake.git /hb
WORKDIR /hb

RUN sed -i \
        -e 's|nv-codec-headers-13\.0\.19\.0|nv-codec-headers-12.2.72.0|g' \
        -e 's|/n13\.0\.19\.0/|/n12.2.72.0/|g' \
        -e 's|releases/download/contribs2/nv-codec-headers-12\.2\.72\.0\.tar\.gz|releases/download/n12.2.72.0/nv-codec-headers-12.2.72.0.tar.gz|' \
        -e 's|13da39edb3a40ed9713ae390ca89faa2f1202c9dda869ef306a8d4383e242bee|c295a2ba8a06434d4bdc5c2208f8a825285210d71d91d572329b2c51fd0d4d03|' \
        contrib/nvenc/module.defs

RUN ./configure --launch-jobs=$(nproc) --launch \
        --enable-nvenc --enable-nvdec --enable-qsv \
        --disable-gtk \
    && cp build/HandBrakeCLI /HandBrakeCLI \
    && strip /HandBrakeCLI

FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive \
    RECODARR_DATA_DIR=/data \
    RECODARR_ADDR=:8080

RUN echo "deb http://deb.debian.org/debian trixie main contrib non-free non-free-firmware" > /etc/apt/sources.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata \
        libnuma1 libass9 libfontconfig1 libfreetype6 libfribidi0 \
        libharfbuzz0b libjansson4 libxml2 libgnutls30 \
        libmp3lame0 libopus0 libtheora0 libvorbisenc2 libvorbis0a \
        libsamplerate0 libspeex1 libvpx9 libx264-164 libturbojpeg0 \
        libva-drm2 libva2 vainfo \
        libvpl2 libmfx-gen1.2 \
        intel-media-va-driver-non-free \
        mesa-va-drivers \
        ffmpeg \
    && rm -rf /var/lib/apt/lists/*

COPY --from=handbrake-builder /HandBrakeCLI /usr/local/bin/HandBrakeCLI

RUN groupadd -r recodarr && useradd -r -g recodarr -u 10001 -d /data -s /usr/sbin/nologin recodarr \
    && mkdir -p /data /media \
    && chown -R recodarr:recodarr /data

COPY --from=go-builder /out/recodarr /usr/local/bin/recodarr
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod +x /usr/local/bin/docker-entrypoint.sh

EXPOSE 8080
VOLUME ["/data"]
USER recodarr
ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
