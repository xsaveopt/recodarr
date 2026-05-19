#!/bin/sh
# Two GPU-related startup fixups, both no-ops on hosts without a render node:
#
# 1. Auto-export LIBVA_DRIVER_NAME based on the PCI vendor of /dev/dri/renderD128
#    so libva initialises cleanly in a containerised, non-X environment without
#    relying on driver auto-enumeration. User-set env wins.
#
# 2. Work around a HandBrake-on-Linux bug (libhb/hwaccel.c) where the FFmpeg QSV
#    child_device hint is written as a bare integer ("0"). On Windows that's a
#    d3d11va adapter index; on Linux the child is VAAPI which treats it as a
#    path, calls open("0"), fails. We `cd` into a dir containing 0→renderD128,
#    1→renderD129, … so the relative open() resolves. Remove when upstream
#    HandBrake passes a real /dev/dri path.

set -e

if [ -z "${LIBVA_DRIVER_NAME:-}" ] && [ -e /dev/dri/renderD128 ]; then
    for dev in /sys/class/drm/renderD*/device/vendor; do
        [ -r "$dev" ] || continue
        case "$(cat "$dev" 2>/dev/null)" in
            0x8086) export LIBVA_DRIVER_NAME=iHD ;;
            0x1002) export LIBVA_DRIVER_NAME=radeonsi ;;
        esac
        break
    done
fi

QSV_CWD=/tmp/recodarr-qsv-cwd
if mkdir -p "$QSV_CWD" 2>/dev/null; then
    for render in /dev/dri/renderD*; do
        [ -e "$render" ] || continue
        idx=$(($(basename "$render" | sed 's/renderD//') - 128))
        ln -sfn "$render" "$QSV_CWD/$idx" 2>/dev/null || true
    done
    cd "$QSV_CWD" 2>/dev/null || true
fi

exec /usr/local/bin/recodarr "$@"
