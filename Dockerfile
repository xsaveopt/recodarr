# syntax=docker/dockerfile:1.7

ARG NODE_VERSION=24
ARG GO_VERSION=1.26
ARG HANDBRAKE_VERSION=1.11.1

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

# Build HandBrakeCLI. --launch fetches and statically links HandBrake's contrib
# deps (x265, dav1d, ffmpeg, nv-codec-headers, etc.) so the resulting binary has
# minimal runtime library requirements.
RUN git clone --depth=1 -b ${HANDBRAKE_VERSION} \
        https://github.com/HandBrake/HandBrake.git /hb
WORKDIR /hb

# Pin nv-codec-headers to the API-12.2 line (n12.2.72.0). HandBrake 1.10+ ships
# 13.0.19.0 by default, which requires NVIDIA driver 570+. The 12.2 headers
# work with driver 550+ which covers Tesla P4 / Pascal hosts in the wild. The
# old contribs2 mirror URL doesn't host the older tarball anymore, so we point
# both URLs at FFmpeg's release archive directly.
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
        libsamplerate0 libspeex1 libvpx9 libx264-164 libturbojpeg0 \
        # VAAPI core. libva auto-loads the driver matching the PCI vendor of
        # whatever render node is passed in, so we install one driver per vendor
        # and they co-exist harmlessly (only the matching one ever gets loaded).
        libva-drm2 libva2 vainfo \
        # Intel QSV stack. libvpl2 is just the oneVPL dispatcher; on its own
        # it finds zero implementations and HandBrake returns MFX session = -9.
        # The actual GPU runtime is libmfx-gen (Gen ≥9, includes Arc); libmfxgen1
        # is the trixie package name. intel-media-va-driver-non-free is the iHD
        # driver — required for Arc AV1 (encode + decode) and any Gen ≥9 QSV.
        # Note: GuC/HuC firmware for Arc must be installed on the HOST
        # (firmware-misc-nonfree on Debian, linux-firmware on most distros) —
        # the container can't inject firmware into the host kernel.
        libvpl2 libmfxgen1 \
        intel-media-va-driver-non-free \
        # AMD: Mesa's gallium VAAPI driver (radeonsi). Covers VCN encode on
        # RDNA/Vega and decode on older parts. AMD has no equivalent of QSV;
        # HandBrake's vce_* encoders go through this libva path.
        mesa-va-drivers \
        # ffprobe for the per-profile pre-encode filters (codec detection,
        # bitrate, HDR transfer, resolution). The full ffmpeg package would
        # work but ships ~200 MB of codecs we don't use; ffmpeg-bin gives us
        # just the binaries. If apt's split makes ffprobe-only impossible
        # on this Debian release we fall back to the umbrella ffmpeg package.
        ffmpeg \
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
