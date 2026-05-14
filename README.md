# Recodarr

Auto re-encodes downloaded series and movies via HandBrake. Sits alongside Sonarr/Radarr/qBittorrent and re-encodes imported files in place once seeding is done.

## How it works

Sonarr or Radarr imports a file and POSTs a webhook to Recodarr. If the item carries a tag that you've mapped to a profile, Recodarr queues a job. Every 30 seconds the worker checks qBittorrent: when the torrent is no longer seeding, the job becomes ready. The worker then runs HandBrakeCLI on the imported library file, writes to a sibling temp file, and atomically renames over the original. Finally it asks *arr to refresh so the new file size shows up.

Files without a mapped tag are ignored.

## First-run setup

Start the container, open `http://<host>:8080`, and you'll be sent to the setup screen to create the single admin user. After that you log in with those credentials. Forgot the password later? Run `recodarr reset-admin` inside the container, then visit the app to set up a new one.

## docker-compose

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
      # Paths inside the container must match what Sonarr/Radarr send.
      - /srv/media:/srv/media
```

```bash
docker compose up -d
```

See the GPU acceleration section below to add hardware encode/decode.

## Wiring it up

1. **HandBrake profile**: Settings, HandBrake Profiles. Pick an encoder and quality. The profile controls how files get re-encoded.

2. **qBittorrent**: Settings, qBittorrent. Add your qBit URL and credentials. Without this, jobs sit in `waiting_for_seed` forever.

3. **Sonarr / Radarr**: Settings, Sonarr / Radarr. Add an instance. After saving, click **Show** on the instance row to reveal the webhook URL plus a generated username and password. In *arr, go to Settings, Connect, add a Webhook with:
   - URL: as shown
   - Method: POST
   - Triggers: tick **On File Import** (and **On File Upgrade** if you also want re-encodes after a quality upgrade)
   - Username and Password: as shown

   Auth is required. Unauthenticated webhooks are rejected.

4. **Tag your items**: in *arr, create a tag (e.g. `recodarr`) and apply it to the series or movies you want re-encoded.

5. **Mapping**: Settings, Mappings. Add a row pointing the tag at a profile. Items without a matching tag are ignored.

## Encoding window

Settings, Worker. Set a start and end time in `HH:MM` to restrict when encodes can run. Outside the window, jobs sit at `ready` until the window opens. Leave both blank to encode any time.

## Failed jobs

Failed jobs are listed on the Jobs page. Click the error message to see the full HandBrake output. Use Retry to re-queue. A job that fails to start five times in a row is given up on automatically; retrying resets the counter. If the encode succeeds but the *arr refresh call fails, the job is marked done and the refresh error is shown next to it.

## GPU acceleration

The image is built with hardware encode and decode for all three vendors. Add the snippet for your card to the `recodarr` service in `docker-compose.yml`, then pick the matching encoder in your HandBrake profile. Verify the GPU is detected on Settings, Debug.

For each vendor: encoder names use that family's prefix (`nvenc_`, `qsv_`, `vce_`). Decode is automatic on the same family if you tick **Hardware Decode** in the profile.

### NVIDIA (NVENC + NVDEC)

Host: install `nvidia-container-toolkit`, then `sudo nvidia-ctk runtime configure --runtime=docker && sudo systemctl restart docker`. Verify with `nvidia-smi` on the host.

```yaml
    environment:
      NVIDIA_VISIBLE_DEVICES: all
      NVIDIA_DRIVER_CAPABILITIES: compute,video,utility
    deploy:
      resources:
        reservations:
          devices:
            - driver: nvidia
              count: all
              capabilities: [gpu]
```

Encoders: `nvenc_h264`, `nvenc_h265`, `nvenc_h265_10bit`, `nvenc_av1` (Ada and newer).

Pascal cards (P4/P40/P100/GTX 10xx) do not support `temporal-aq=1`, HEVC B-frames, or AV1. Stick to `spatial-aq=1:rc-lookahead=16` in extra args, leave Tune empty, and pick the `slow` or `medium` preset.

### Intel (QSV + VAAPI)

Host: nothing special. Confirm `/dev/dri/renderD128` exists.

```yaml
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - "104"   # render, match host: getent group render | cut -d: -f3
      - "44"    # video
```

Encoders: `qsv_h264`, `qsv_h265`, `qsv_h265_10bit`, `qsv_av1` (Arc / 11th-gen+).

Older iGPUs (Haswell through Skylake) only do H.264 reliably; HEVC needs Kaby Lake or newer.

### AMD (VCE via VAAPI)

Host: nothing special. Confirm `/dev/dri/renderD128` exists.

```yaml
    devices:
      - /dev/dri:/dev/dri
    group_add:
      - "104"
      - "44"
```

Encoders: `vce_h264`, `vce_h265`, `vce_h265_10bit`, `vce_av1` (RX 7000 series and newer).

### Apple

VideoToolbox only works when running the Recodarr binary natively on macOS, not in a Linux container. Not supported in Docker.

## Image tags

`latest` for the latest stable release. `1`, `1.2`, `1.2.3` to pin to a major, minor, or patch line. Pre-releases like `1.2.3-rc1` are never tagged `latest`. `dev` tracks the tip of the `main` branch (rebuilt on every commit) and is the easiest tag to use for testing without waiting for a release. Per-commit immutable tags are also published as `dev-<sha>`. Images are published to `ghcr.io/sratabix/recodarr` and built for `linux/amd64`.

## Environment variables

`RECODARR_ADDR` (default `:8080`), `RECODARR_DATA_DIR` (default `/data`, mount this), `TZ` for log timestamps. Everything else lives in SQLite and is managed through the web UI.

## CLI

```
recodarr               start the server
recodarr reset-admin   wipe the admin user, then visit the app to set up again
recodarr help          show usage
```
