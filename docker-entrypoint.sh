#!/bin/sh

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
