# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=24
ARG GO_VERSION=1.26
ARG HANDBRAKE_VERSION=1.11.1
ARG NVCODEC_HEADERS_VERSION=n12.2.72.0

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

# 3. Build HandBrakeCLI from source with NVENC + NVDEC + QSV support.
#    The Debian-shipped handbrake-cli is built without NVDEC, so on a Pascal P4
#    the encode runs on the GPU but the decode falls back to CPU and becomes the
#    bottleneck. Building from source with --enable-nvdec fixes that.
#    nv-codec-headers gives us the NVENC/NVDEC SDK headers without dragging in
#    the full multi-GB CUDA toolkit.
#    Builder base must match the runtime base (debian:trixie-slim) so the
#    binary's glibc requirements line up. Trixie ships gcc 13, which avoids a
#    gcc-12 -Wmaybe-uninitialized false positive in zimg's AVX-512 intrinsics
#    that breaks the build on bookworm.
FROM debian:trixie-slim AS handbrake-builder
ARG HANDBRAKE_VERSION
ARG NVCODEC_HEADERS_VERSION
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

# Install nv-codec-headers (NVENC + NVDEC SDK headers, no CUDA install needed).
RUN git clone --depth=1 -b ${NVCODEC_HEADERS_VERSION} \
        https://github.com/FFmpeg/nv-codec-headers.git /tmp/nvcodec \
    && make -C /tmp/nvcodec install \
    && rm -rf /tmp/nvcodec

# Build HandBrakeCLI. --launch fetches and statically links HandBrake's contrib
# deps (x265, dav1d, ffmpeg, etc.) so the resulting binary has minimal runtime
# library requirements.
RUN git clone --depth=1 -b ${HANDBRAKE_VERSION} \
        https://github.com/HandBrake/HandBrake.git /hb
WORKDIR /hb
RUN ./configure --launch-jobs=$(nproc) --launch \
        --enable-nvenc --enable-nvdec --enable-qsv \
        --disable-gtk \
    && cp build/HandBrakeCLI /HandBrakeCLI \
    && strip /HandBrakeCLI

# 4. Runtime — Debian trixie-slim with HandBrakeCLI + Intel/AMD VAAPI/QSV libs.
#    NVENC/NVDEC work at runtime via nvidia-container-toolkit; the toolkit
#    injects libcuda + libnvcuvid + libnvidia-encode based on the container's
#    NVIDIA_DRIVER_CAPABILITIES env var (must include compute,video,utility).
FROM debian:trixie-slim
ENV DEBIAN_FRONTEND=noninteractive \
    RECODARR_DATA_DIR=/data \
    RECODARR_ADDR=:8080

RUN echo "deb http://deb.debian.org/debian trixie main contrib non-free non-free-firmware" > /etc/apt/sources.list \
    && apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates tzdata \
        # HandBrake runtime shared libraries (most contrib deps are statically
        # linked via --launch but a few system libs remain dynamic).
        libnuma1 libass9 libfontconfig1 libfreetype6 libfribidi0 \
        libharfbuzz0b libjansson4 libxml2 libgnutls30 \
        libmp3lame0 libopus0 libtheora0 libvorbisenc2 libvorbis0a \
        libsamplerate0 libspeex1 \
        # Intel/AMD VAAPI + QSV runtime
        libva-drm2 libva2 libvpl2 vainfo \
        intel-media-va-driver \
    && rm -rf /var/lib/apt/lists/*

COPY --from=handbrake-builder /HandBrakeCLI /usr/local/bin/HandBrakeCLI

RUN groupadd -r recodarr && useradd -r -g recodarr -u 10001 -d /data -s /usr/sbin/nologin recodarr \
    && mkdir -p /data /media \
    && chown -R recodarr:recodarr /data

COPY --from=go-builder /out/recodarr /usr/local/bin/recodarr

EXPOSE 8080
VOLUME ["/data"]
USER recodarr
ENTRYPOINT ["/usr/local/bin/recodarr"]
