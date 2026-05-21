# NVIDIA GPU acceleration

This guide gets NVENC and NVDEC working inside the Recodarr container so HandBrake can encode on your GPU instead of CPU. Tested with the standard `ghcr.io/sratabix/recodarr` image.

## What you get

GPU encode (NVENC) and decode (NVDEC) for the matching codec family. Concretely: any Recodarr profile whose encoder name starts with `nvenc_` (`nvenc_h264`, `nvenc_h265`, `nvenc_h265_10bit`, `nvenc_av1`) will run on the GPU. Decode is auto-enabled on the same family — Recodarr passes `--enable-hw-decoding nvdec` to HandBrake whenever you pick an `nvenc_*` encoder, so the entire pipeline is zero-copy on the card.

## Prerequisites

NVENC/NVDEC are different from Intel/AMD: the userspace libraries (`libnvidia-encode`, `libnvcuvid`, …) are version-locked to the kernel driver, so the Container Toolkit bind-mounts them in from the host at runtime rather than the image shipping its own copy. That means the host setup is non-negotiable:

1. **NVIDIA proprietary driver** installed and working. `nvidia-smi` on the host must succeed.
2. **NVIDIA Container Toolkit** so Docker can hand the GPU to a container.
3. **Docker configured to use the nvidia runtime** (one command after toolkit install).

You do **not** install CUDA, `nvidia-encode`, or HandBrake on the host — those come from the image and the toolkit's mounts. Driver minimums for current NVENC features: **Linux 535+** is the safe floor. Earlier drivers may work but won't expose AV1 on Ada cards.

### Install the driver

Use your distro's repo, *not* `.run` files. On Debian/Ubuntu:

```bash
sudo apt install nvidia-driver-575      # or whatever the current LTS branch is
sudo reboot
nvidia-smi                              # must show your card
```

### Install the Container Toolkit

Follow Nvidia's official guide once — it changes apt repo URLs occasionally so the upstream doc is more reliable than copy-pasting here:
<https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/install-guide.html>

After install, register it as Docker's runtime and restart the daemon:

```bash
sudo nvidia-ctk runtime configure --runtime=docker
sudo systemctl restart docker
```

Verify with the official CUDA image:

```bash
docker run --rm --gpus all nvidia/cuda:12.6.0-base-ubuntu22.04 nvidia-smi
```

If that prints your GPU, the host is ready. If it doesn't, no Recodarr config will help — fix the host first.

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
      # ── NVIDIA ───────────────────────────────────────
      NVIDIA_VISIBLE_DEVICES: all
      NVIDIA_DRIVER_CAPABILITIES: compute,video,utility
      # ─────────────────────────────────────────────────
    volumes:
      - ./recodarr-data:/data
      - /srv/media:/srv/media
    # ── NVIDIA ─────────────────────────────────────────
    runtime: nvidia
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu, compute, video, utility]
    # ───────────────────────────────────────────────────
```

About each piece:

- `runtime: nvidia` selects Docker's NVIDIA runtime (registered above).
- `NVIDIA_DRIVER_CAPABILITIES=compute,video,utility` is the **minimum** for NVENC/NVDEC. `video` is the load-bearing one; `compute` enables CUDA contexts that some HandBrake operations create internally; `utility` lets `nvidia-smi` work inside the container, which is invaluable for debugging. You can use `all` instead — it's a tiny image-size penalty in exchange for never having to think about which capability is missing.
- `count: all` is a `deploy` shorthand for "every GPU". If you have multiple cards and want to pin Recodarr to one, set `device_ids: ['0']` instead.
- `capabilities: [gpu, compute, video, utility]` mirrors the env-var list. The two are checked independently by the toolkit, and missing either side leads to confusing errors.

After bringing the stack up, **verify NVENC is actually visible from inside the container**:

```bash
docker exec recodarr nvidia-smi
```

You should see your card and any in-flight processes. If `nvidia-smi: command not found`, you forgot `utility` in `NVIDIA_DRIVER_CAPABILITIES`.

## Detection in Recodarr

Open Recodarr → Debug. You should see:

- **NVIDIA NVENC: available**
- One or more `nvenc_*` encoders in the **Detected encoders** list.

If "available" is no but `nvidia-smi` worked from the previous step, the driver is exposed but HandBrake's NVENC plugin couldn't initialize — usually a driver-version mismatch. Update the host driver to the latest LTS branch.

## What works on which card

Short summary below. For the authoritative per-chip breakdown (including session limits, max resolution per codec, and B-frame support), see NVIDIA's official matrix: <https://developer.nvidia.com/video-encode-decode-support-matrix>

| Generation | Cards | H.264 | HEVC 8-bit | HEVC 10-bit | AV1 enc |
|---|---|---|---|---|---|
| Pascal | GTX 10-series, P4/P40/P100 | ✓ | ✓ | ✓ | — |
| Turing | GTX 16-series, RTX 20-series, T4 | ✓ | ✓ | ✓ | — |
| Ampere | RTX 30-series, A series | ✓ | ✓ | ✓ | — |
| Ada Lovelace | RTX 40-series, L4, L40 | ✓ | ✓ | ✓ | ✓ |
| Blackwell | RTX 50-series | ✓ | ✓ | ✓ | ✓ |

**Pascal-specific caveats** (GTX 10xx, P-series Quadro/Tesla):
- No `temporal-aq=1` support. HandBrake either errors or silently ignores it.
- No HEVC B-frames. Don't add `bf=N` in extra args.
- No AV1 anywhere — software encode only.
- The seeded **Live action — nvenc** profile uses `temporal-aq=1` in its extra args. **Edit the profile and remove it** if you're on Pascal, or you'll get encode failures.
- Recommended extra args on Pascal: `--encopts spatial-aq=1:rc-lookahead=16`

## Recodarr profiles to pair this with

The seeded profiles `Modern anime — nvenc` and `Live action — nvenc` use `nvenc_h265_10bit`. They work on Pascal (with the caveat above) and everything newer.

For AV1 on Ada/Blackwell, switch the encoder to `nvenc_av1_10bit` and bump CRF — NVENC AV1 at CRF 32–34 typically matches HEVC's CRF 28–30 in size for similar visual quality.

## Troubleshooting

**`nvidia-smi` works on host, fails in container**
The toolkit isn't registered as a Docker runtime. Re-run `sudo nvidia-ctk runtime configure --runtime=docker` and `sudo systemctl restart docker`.

**`nvidia-smi: command not found` inside container**
`NVIDIA_DRIVER_CAPABILITIES` doesn't include `utility`. Add it.

**`CUDA_ERROR_NO_DEVICE` in HandBrake logs**
Older driver/toolkit combo. The fix is usually to also pass the device nodes explicitly:

```yaml
    devices:
      - /dev/nvidia0
      - /dev/nvidiactl
      - /dev/nvidia-uvm
      - /dev/nvidia-uvm-tools
      - /dev/nvidia-modeset
      - /dev/nvidia-caps
```

This shouldn't be necessary on a current toolkit but it's the canonical workaround.

**Encodes start but the process is on CPU (slow, no GPU usage in `nvidia-smi`)**
The profile's encoder isn't an `nvenc_*` one. Check **Settings → HandBrake Profiles** and make sure the profile mapped to your tag uses an NVENC encoder. The Recodarr **Debug** page lists which encoders the binary actually has compiled in.

**`--temporal-aq` causes failure**
You're on Pascal. Edit the profile's Extra args and remove the `temporal-aq=1` token. See the Pascal section above.

**`recodarr_handbrake_available` Prometheus metric is `0`**
HandBrakeCLI itself isn't on PATH inside the container. This isn't an NVIDIA issue — file a bug because the official image always ships HandBrakeCLI.

## References

- NVIDIA Container Toolkit: <https://docs.nvidia.com/datacenter/cloud-native/container-toolkit/>
- NVENC / NVDEC support matrix (per-GPU codec capabilities, session limits, max resolutions): <https://developer.nvidia.com/video-encode-decode-support-matrix>
- HandBrake NVENC documentation: <https://handbrake.fr/docs/en/latest/technical/video-nvenc.html>
- Jellyfin NVIDIA guide (excellent baseline that this guide draws from): <https://jellyfin.org/docs/general/post-install/transcoding/hardware-acceleration/nvidia/>
