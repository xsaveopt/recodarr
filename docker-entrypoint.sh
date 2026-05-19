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

# HandBrake QSV-on-Linux workaround: libhb/hwaccel.c passes the FFmpeg QSV
# child device hint as a bare integer ("0", "1", ...). That's correct for the
# Windows d3d11va child but on Linux the child is VAAPI, which interprets the
# string as a filesystem path and calls open("0", O_RDWR). The open fails and
# the encode dies with "No VA display found for device 0".
#
# We work around it by setting the worker's CWD to a directory where "0",
# "1", ... are symlinks to the matching /dev/dri/renderD12N nodes. FFmpeg's
# open() then resolves to the right render node, libva initializes, QSV runs.
#
# Track HandBrake upstream — when their Linux path passes a proper /dev/dri
# path, this can go away.
QSV_CWD=/tmp/recodarr-qsv-cwd
if mkdir -p "$QSV_CWD" 2>/dev/null; then
    linked=""
    for render in /dev/dri/renderD*; do
        [ -e "$render" ] || continue
        num=$(basename "$render" | sed 's/renderD//')
        idx=$((num - 128))
        if ln -sfn "$render" "$QSV_CWD/$idx" 2>/dev/null; then
            linked="$linked $idx→$(basename "$render")"
        fi
    done
    if [ -n "$linked" ]; then
        log "QSV child_device workaround symlinks:$linked (cwd=$QSV_CWD)"
        cd "$QSV_CWD" || log "could not chdir to $QSV_CWD — workaround inactive"
    fi
fi

exec /usr/local/bin/recodarr "$@"
