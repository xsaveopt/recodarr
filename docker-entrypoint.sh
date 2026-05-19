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

if [ -z "${LIBVA_DRIVER_NAME:-}" ] && [ -e /dev/dri/renderD128 ]; then
    for dev in /sys/class/drm/renderD*/device/vendor; do
        [ -r "$dev" ] || continue
        vendor=$(cat "$dev" 2>/dev/null || true)
        case "$vendor" in
            0x8086) export LIBVA_DRIVER_NAME=iHD ;;     # Intel
            0x1002) export LIBVA_DRIVER_NAME=radeonsi ;; # AMD
        esac
        break
    done
fi

exec /usr/local/bin/recodarr "$@"
