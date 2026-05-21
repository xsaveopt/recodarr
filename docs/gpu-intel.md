# Intel GPU acceleration (QSV / VA-API)

This guide gets Quick Sync Video (QSV) and VA-API working inside the Recodarr container so HandBrake can encode and decode on your Intel iGPU or discrete Arc card.

## What you get

QSV encode and decode for the matching codec family. Concretely: Recodarr profiles whose encoder name starts with `qsv_` (`qsv_h264`, `qsv_h265`, `qsv_h265_10bit`, `qsv_av1`, `qsv_av1_10bit`) run on the GPU. VA-API is the underlying Linux interface; QSV uses it on modern hardware. Decode is auto-enabled for the same family — Recodarr passes `--enable-hw-decoding qsv` to HandBrake when you pick a `qsv_*` encoder.

If your card is AMD, the same `/dev/dri` passthrough enables `vce_*` encoders via VAAPI — the device-node setup below is identical, only the encoder choice in your profile changes.

## Prerequisites

The container ships the full VAAPI / QSV userspace (iHD driver, `libvpl2` + `libmfx-gen1.2`, Mesa VA drivers, `vainfo`) — **you do not install any of those on the host**. What the host has to provide is the kernel side, since the container can't inject kernel modules or firmware:

1. **A working Intel GPU**. Confirm `/dev/dri/renderD128` exists. If it doesn't, the iGPU is disabled in BIOS or the kernel didn't load `i915` / `xe`.
2. **The right kernel + driver combo for your GPU generation** (table below). Older distros sometimes ship kernels too old for newer Intel hardware.
3. **GPU firmware available to the kernel** (GuC/HuC blobs). On Debian/Ubuntu install `firmware-misc-nonfree`; most other distros ship it in `linux-firmware`. Required for LP encode on i915 and for `xe` on Arc/Lunar/Battlemage.

### Kernel / driver matrix

| Generation | Kernel module | Min kernel |
|---|---|---|
| Broadwell – Tiger Lake (Gen 9 – Gen 12) | `i915` | 5.15+ is fine |
| Alder Lake / Raptor Lake (Gen 12) | `i915` | 5.17+ |
| Arc A-series (DG2) | `i915` | 6.2+ recommended |
| Meteor Lake | `i915` | 6.7+ |
| Lunar Lake / Battlemage / Arc B-series | `xe` | **6.12+** |

Run `uname -r` to check. If your kernel is older than the minimum for your GPU, the device node may exist but encode/decode will fail in confusing ways. Upgrade the kernel before chasing other bugs.

### Host packages

For Recodarr itself, **none** beyond the firmware mentioned above — the VAAPI/QSV userspace lives inside the container.

If you want to sanity-check the GPU from the host before bringing the container up, install `vainfo` (and the matching driver, since `vainfo` needs one to talk to):

```bash
sudo apt install vainfo intel-media-va-driver-non-free   # Debian / Ubuntu, optional
```

These are diagnostic-only; the container has its own copy and ignores whatever you install on the host. The `non-free` variant is the right one — `intel-media-va-driver` (without `non-free`) is missing the encoder bits.

### Verify on the host (optional)

If you installed `vainfo`:

```bash
vainfo --display drm --device /dev/dri/renderD128
```

You should see a list of profiles ending with entries like `VAProfileHEVCMain10`, `VAProfileAV1Profile0` and **both** `VAEntrypointEncSlice` (regular encode) and `VAEntrypointEncSliceLP` (low-power encode) for each profile your card supports. If `vainfo` returns "no encoders" or a tiny list, the host kernel/driver setup is wrong — fix it before moving on. If you skipped the host install, jump straight to the in-container verify further down.

### Find the render group ID

The container needs to be in the same group that owns `/dev/dri/renderD128` on the host:

```bash
getent group render | cut -d: -f3
# 104 on Debian, 109 on Ubuntu, 989 on some Fedora setups — varies
ls -l /dev/dri/renderD128
# crw-rw---- 1 root render 226, 128 ... — confirm the second group is "render"
```

Save that number; you'll use it in compose. On a few distros the device is owned by `video` instead, in which case use that group's GID.

## docker-compose snippet

Add the highlighted block to your existing `recodarr` service:

```yaml
services:
  recodarr:
    image: ghcr.io/sratabix/recodarr:latest
    container_name: recodarr
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      TZ: Europe/Amsterdam
    volumes:
      - ./recodarr-data:/data
      - /srv/media:/srv/media
    # ── Intel ──────────────────────────────────────────
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - "104"        # render group GID — replace with `getent group render`
      - "44"         # video group GID — almost always 44, harmless if unused
    # ───────────────────────────────────────────────────
```

About each piece:

- **`/dev/dri:/dev/dri`** maps the whole DRI directory rather than just `renderD128`. This is intentional: some setups (especially Arc + dual-card, or hosts that hot-plug a display) shuffle device-node numbering, and mapping the parent directory avoids breakage. There's no security difference — render nodes are the only writable bits and they're already gated by group permission.
- **`group_add: ["104"]`** is the part everyone gets wrong. Without this the container's process can see `/dev/dri/renderD128` but can't open it (`Permission denied` from VAAPI). The number must match the host's render group GID. Quotes are required because compose treats unquoted numbers as ints and chokes.
- **`group_add: ["44"]`** for the `video` group is harmless on most setups but required on a few older distros where the device is owned by `video` instead of `render`.

No environment variables are needed — VAAPI auto-discovers the iGPU through `/dev/dri`, and Recodarr asks HandBrake to use it explicitly via `--enable-hw-decoding qsv`.

## Verify inside the container

```bash
docker exec recodarr vainfo --display drm --device /dev/dri/renderD128
```

If the list mirrors what you saw on the host, you're done. If you get `Permission denied`, the `group_add` GID is wrong — recheck `getent group render`. If you get `No such file or directory`, the `devices:` mapping didn't take effect — restart the stack.

## Detection in Recodarr

Open Recodarr → Debug. You should see:

- **Intel QSV: available**
- **VAAPI (Linux DRI): available**
- One or more `qsv_*` encoders in the **Detected encoders** list.

If "available" is no but `vainfo` works inside the container, HandBrake's QSV plugin couldn't initialize. Most common cause is missing `intel-opencl-icd` on a card that needs it (Arc and newer).

## What works on which card

| Generation | Cards / CPUs | H.264 | HEVC 8-bit | HEVC 10-bit | AV1 dec | AV1 enc |
|---|---|---|---|---|---|---|
| Haswell – Skylake (Gen 9) | 4th–6th gen Core | ✓ | ✓ (limited) | — | — | — |
| Kaby Lake – Coffee Lake (Gen 9.5) | 7th–10th gen Core | ✓ | ✓ | ✓ | — | — |
| Ice Lake – Tiger Lake (Gen 11–12) | 10th–11th gen Core | ✓ | ✓ | ✓ | ✓ (TGL+) | — |
| Alder Lake – Raptor Lake (Gen 12) | 12th–14th gen Core | ✓ | ✓ | ✓ | ✓ | — |
| Arc A-series (DG2) | A380, A750, A770 | ✓ | ✓ | ✓ | ✓ | ✓ |
| Meteor / Lunar Lake (Xe-LPG / Xe2) | Core Ultra | ✓ | ✓ | ✓ | ✓ | ✓ (LNL+) |
| Battlemage / Arc B-series (Xe2) | B580 | ✓ | ✓ | ✓ | ✓ | ✓ |

**Rules of thumb**:
- HEVC encode reliably starts at **Kaby Lake (7th gen)**. Older chips theoretically support it but the drivers are flaky.
- AV1 encode is **Arc-class only** (DG2 onward). All earlier QSV silicon will fail at encode time with cryptic "encoder not initialized" errors.
- For Arc B-series and Lunar Lake, you **must** be on kernel 6.12+ with the `xe` driver. The `i915` fallback won't drive these cards correctly.

## Low-power encoding (LP mode)

Most QSV-capable iGPUs from Kaby Lake onwards expose a parallel low-power encode path that uses fixed-function hardware instead of the EUs. It's roughly 2–3× faster at slightly lower quality. Arc and newer **only** have the LP path — there's no choice. To enable on i915 hardware, load the GuC firmware:

```bash
sudo sh -c "echo 'options i915 enable_guc=2' > /etc/modprobe.d/i915-guc.conf"
sudo update-initramfs -u
sudo reboot
```

Verify after reboot:

```bash
sudo dmesg | grep -E "i915.*GuC|i915.*HuC"
# expect: "GuC firmware ... submission enabled" and "HuC firmware ... loaded"
```

HandBrake decides whether to use LP or full-power encode automatically based on what the driver exposes. There's nothing to configure in Recodarr — if LP is available, it'll be used.

For the `xe` driver (Arc B / Lunar Lake / Meteor Lake on newer kernels), GuC/HuC loading is automatic and you don't need to do any of this.

## Recodarr profiles to pair this with

The seeded profiles are tuned for current hardware:

- **`Modern anime — qsv (HEVC)`** and **`Live action — qsv (HEVC)`** — Kaby Lake and newer, all 10-bit HEVC.
- **`Modern anime — qsv (AV1)`** and **`Live action — qsv (AV1)`** — Arc / Lunar Lake / Battlemage only.

If you're on pre-Kaby Lake hardware, edit the QSV profiles to use `qsv_h264` instead, drop the CQ to ~22, and don't expect AV1 to work.

## Troubleshooting

**`vainfo` works on host, fails in container with `Permission denied`**
Group ID mismatch. Re-run `getent group render` on the host and update `group_add`.

**`vainfo` works in container but Recodarr says "QSV: not detected"**
HandBrake's QSV plugin failed to initialize even though VAAPI is fine. The Recodarr image already ships `libvpl2`, `libmfx-gen1.2` and the `iHD` driver, so this is almost always a kernel/firmware gap on the host — verify the kernel matches the matrix above and that `firmware-misc-nonfree` (GuC/HuC) is installed.

**Encode starts but is implausibly slow (CPU usage high, GPU idle)**
The profile is using a software encoder. Check **Settings → HandBrake Profiles** — the encoder field must start with `qsv_` (or `vce_` for AMD).

**`/dev/dri/renderD128` doesn't exist**
Either the iGPU is disabled in BIOS, the kernel module didn't load, or you're on a server without integrated graphics. `lspci | grep -iE 'vga|display'` to check what hardware is actually present, `dmesg | grep -E 'i915|xe'` to see what the driver did at boot.

**Arc B-series / Lunar Lake encodes crash or produce garbage**
Kernel too old. You need 6.12+ and the `xe` driver, and Resizable BAR enabled in BIOS. `dmesg | grep xe` should show the xe driver bound your card.

**Multiple GPUs (e.g. iGPU + Arc dGPU)**
The first device is `renderD128`, the second is `renderD129`. Map only the one you want by replacing `/dev/dri:/dev/dri` with the specific node. To pin Recodarr to the dGPU:

```yaml
    devices:
      - /dev/dri/renderD129:/dev/dri/renderD128
```

That remaps it to the canonical name HandBrake expects, so no other config changes are needed.

## References

- Intel media driver (iHD): <https://github.com/intel/media-driver>
- Intel compute runtime (OpenCL): <https://github.com/intel/compute-runtime>
- HandBrake QSV documentation: <https://handbrake.fr/docs/en/latest/technical/video-qsv.html>
- Jellyfin Intel guide (excellent baseline that this guide draws from): <https://jellyfin.org/docs/general/post-install/transcoding/hardware-acceleration/intel/>
