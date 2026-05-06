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
    # Intel iGPU (QSV/VAAPI), uncomment for hardware acceleration:
    # devices:
    #   - /dev/dri:/dev/dri
    # group_add:
    #   - "104"   # render, match host: getent group render | cut -d: -f3
    #   - "44"    # video
```

```bash
docker compose up -d
```

## Wiring it up

1. **HandBrake profile**: Settings, HandBrake Profiles. Pick an encoder and quality. The profile controls how files get re-encoded.

2. **qBittorrent**: Settings, qBittorrent. Add your qBit URL and credentials. Without this, jobs sit in `waiting_for_seed` forever.

3. **Sonarr / Radarr**: Settings, Sonarr / Radarr. Add an instance. After saving you'll see a webhook URL like `http://recodarr:8080/webhook/sonarr/1` and a generated webhook secret. In *arr, go to Settings, Connect, add a Webhook with:
   - URL: the one shown in Recodarr
   - Method: POST
   - Triggers: On File Import (and On Import if available)
   - Headers: add `X-Webhook-Token` with the secret as its value

   The secret is required. Webhooks without it are rejected.

4. **Tag your items**: in *arr, create a tag (e.g. `recodarr`) and apply it to the series or movies you want re-encoded.

5. **Mapping**: Settings, Mappings. Add a row pointing the tag at a profile. Items without a matching tag are ignored.

## Encoding window

Settings, Worker. Set a start and end time in `HH:MM` to restrict when encodes can run. Outside the window, jobs sit at `ready` until the window opens. Leave both blank to encode any time.

## Failed jobs

Failed jobs are listed on the Jobs page. Click the error message to see the full HandBrake output. Use Retry to re-queue. A job that fails to start five times in a row is given up on automatically; retrying resets the counter. If the encode succeeds but the *arr refresh call fails, the job is marked done and the refresh error is shown next to it.

## Hardware acceleration

The image ships with VAAPI and QSV runtime libraries. For Intel/AMD pass `/dev/dri` through and add your host's `render` group GID via `group_add`. For NVIDIA install `nvidia-container-toolkit` on the host and add a `deploy.resources.reservations.devices` block. Apple VideoToolbox only works when running the binary natively on macOS.

Check Settings, Debug to verify the detected encoders.

## Image tags

`latest` for the latest stable release. `1`, `1.2`, `1.2.3` to pin to a major, minor, or patch line. Pre-releases like `1.2.3-rc1` are never tagged `latest`. Images are published to `ghcr.io/sratabix/recodarr` on every `vX.Y.Z` git tag.

## Environment variables

`RECODARR_ADDR` (default `:8080`), `RECODARR_DATA_DIR` (default `/data`, mount this), `TZ` for log timestamps. Everything else lives in SQLite and is managed through the web UI.

## CLI

```
recodarr               start the server
recodarr reset-admin   wipe the admin user, then visit the app to set up again
recodarr help          show usage
```
