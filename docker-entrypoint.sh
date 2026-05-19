#!/bin/sh
# Auto-configure LIBVA_DRIVER_NAME so VAAPI/QSV "just work" on whichever GPU
# the user passed through. FFmpeg's VAAPI hwdevice creation often can't
# enumerate drivers in a containerised, non-X environment; setting the driver
# name explicitly skips that enumeration and points libva at the right .so.
#
# Detection reads /sys/class/drm/renderD*/device/vendor (PCI vendor ID), so it
# works without root and without invoking lspci. NVIDIA never touches libva so
# it's harmless when no DRM node is mapped — we just don't set anything.
#
# Anything the user sets in compose/env wins: the explicit check below only
# fires when LIBVA_DRIVER_NAME is unset.

set -e

log() { echo "[entrypoint] $*" >&2; }

if [ -n "${LIBVA_DRIVER_NAME:-}" ]; then
    log "LIBVA_DRIVER_NAME already set to '$LIBVA_DRIVER_NAME' (from env); leaving as-is"
elif [ ! -e /dev/dri/renderD128 ]; then
    log "no /dev/dri/renderD128 — skipping GPU vendor detection (NVENC or no GPU)"
else
    detected=""
    for dev in /sys/class/drm/renderD*/device/vendor; do
        [ -r "$dev" ] || continue
        vendor=$(cat "$dev" 2>/dev/null || true)
        case "$vendor" in
            0x8086) detected=iHD ;;      # Intel
            0x1002) detected=radeonsi ;; # AMD
            *)      log "unknown GPU vendor '$vendor' at $dev — leaving LIBVA_DRIVER_NAME unset" ;;
        esac
        [ -n "$detected" ] && break
    done
    if [ -n "$detected" ]; then
        export LIBVA_DRIVER_NAME="$detected"
        log "detected GPU vendor → LIBVA_DRIVER_NAME=$detected"
    else
        log "no readable /sys/class/drm/renderD*/device/vendor entries — leaving LIBVA_DRIVER_NAME unset"
    fi
fi

exec /usr/local/bin/recodarr "$@"
