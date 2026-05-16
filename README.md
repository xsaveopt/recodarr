# Recodarr

Auto re-encodes downloaded series and movies via HandBrake. Sits alongside Sonarr/Radarr/qBittorrent and re-encodes imported files in place once seeding is done.

> ⚠️ **Local use only.** Recodarr is built for trusted home/LAN networks — alongside your other *arr stack. Don't expose it to the public internet. The security model is single-admin auth + LAN-only assumptions; it has not been hardened against hostile traffic. If you need it accessible from outside, put it behind a VPN (WireGuard, Tailscale) — never directly on a public IP or a vanilla reverse proxy. The remote agent is even more strictly LAN-only: shared bearer token, plain HTTP, no rate limiting.

## Contents

- [How it works](#how-it-works)
- [First-run setup](#first-run-setup)
- [docker-compose](#docker-compose)
- [Wiring it up](#wiring-it-up)
- [Encoding window](#encoding-window)
- [Failed jobs](#failed-jobs)
- [Prometheus metrics](#prometheus-metrics)
- [GPU acceleration](#gpu-acceleration)
- [Remote encode agent](#remote-encode-agent)
- [Image tags](#image-tags)
- [Environment variables](#environment-variables)
- [CLI](#cli)
- [License](#license)

## How it works

Sonarr or Radarr imports a file and POSTs a webhook to Recodarr. If the item carries a tag that you've mapped to a profile, Recodarr queues a job. Every 30 seconds the worker checks qBittorrent: when the torrent is no longer seeding, the job becomes ready. The worker then runs HandBrakeCLI on the imported library file, writes to a sibling temp file, and atomically renames over the original. Finally it asks \*arr to refresh so the new file size shows up.

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

3. **Sonarr / Radarr**: Settings, Sonarr / Radarr. Add an instance. After saving, click **Show** on the instance row to reveal the webhook URL plus a generated username and password. In \*arr, go to Settings, Connect, add a Webhook with:
   - URL: as shown
   - Method: POST
   - Triggers: tick **On File Import** (and **On File Upgrade** if you also want re-encodes after a quality upgrade)
   - Username and Password: as shown

   Auth is required. Unauthenticated webhooks are rejected.

4. **Tag your items**: in \*arr, create a tag (e.g. `recodarr`) and apply it to the series or movies you want re-encoded.

5. **Mapping**: Settings, Mappings. Add a row pointing the tag at a profile. Items without a matching tag are ignored.

## Encoding window

Settings, Worker. Set a start and end time in `HH:MM` to restrict when encodes can run. Outside the window, jobs sit at `ready` until the window opens. Leave both blank to encode any time.

## Failed jobs

Failed jobs are listed on the Jobs page. Click the error message to see the full HandBrake output. Use Retry to re-queue. A job that fails to start five times in a row is given up on automatically; retrying resets the counter. If the encode succeeds but the \*arr refresh call fails, the job is marked done and the refresh error is shown next to it.

## Prometheus metrics

A Prometheus scrape endpoint is exposed at `/metrics`. It's mounted outside the authenticated API, so scrapers don't need a session cookie. By default it's open — only counters and gauges are emitted, never secrets — but you can require a bearer token by setting `RECODARR_METRICS_TOKEN`.

```yaml
scrape_configs:
  - job_name: recodarr
    static_configs:
      - targets: ["recodarr:8080"]
    # Only if RECODARR_METRICS_TOKEN is set:
    # authorization:
    #   credentials: your-token
```

Series exposed:

| Metric                                        | Type  | Description                                                                             |
| --------------------------------------------- | ----- | --------------------------------------------------------------------------------------- |
| `recodarr_jobs{status}`                       | gauge | Jobs in the queue by status (`waiting_for_seed`, `ready`, `encoding`, `done`, `failed`) |
| `recodarr_bytes_saved_total`                  | gauge | Sum of `original_size − final_size` across completed jobs                               |
| `recodarr_worker_active_encodes`              | gauge | Encodes running right now                                                               |
| `recodarr_worker_max_parallel_encodes`        | gauge | Configured concurrency cap                                                              |
| `recodarr_worker_window_active`               | gauge | `1` if inside the configured encoding window (or no window set), `0` outside            |
| `recodarr_worker_last_tick_timestamp_seconds` | gauge | Unix time of the most recent worker tick                                                |
| `recodarr_handbrake_available`                | gauge | `1` if `HandBrakeCLI` was found on PATH at startup                                      |
| `recodarr_encode_progress_percent{job_id}`    | gauge | Live percent for each in-flight encode                                                  |
| `recodarr_encode_fps{job_id}`                 | gauge | Live FPS for each in-flight encode                                                      |

Plus the standard `go_*` runtime and `process_*` collectors.

## GPU acceleration

The image ships hardware encode and decode for all three vendors. Pick the matching encoder family in your HandBrake profile (`nvenc_*`, `qsv_*`, `vce_*`) and Recodarr enables zero-copy hardware decode automatically. Verify on **Settings → Debug**.

Setup is non-trivial and varies by vendor — full guides:

- **NVIDIA (NVENC + NVDEC)** — see [`docs/gpu-nvidia.md`](docs/gpu-nvidia.md). Container Toolkit, `runtime: nvidia`, capability flags, per-generation codec matrix (Pascal → Blackwell), Pascal-specific gotchas.
- **Intel (QSV + VA-API)** — see [`docs/gpu-intel.md`](docs/gpu-intel.md). `/dev/dri` passthrough, `render` group GID, kernel/driver matrix per generation (Haswell → Battlemage), low-power mode, AV1 hardware encode (Arc and newer).
- **AMD (VCE via VA-API)** — same `/dev/dri` + `group_add` setup as Intel; use the `vce_*` encoder family. The Intel guide's device-passthrough section applies as-is.

## Remote encode agent

> ⚠️ **LAN only.** The agent protocol is one shared bearer token over plain HTTP. Anyone reaching it can submit arbitrary HandBrake commands and read uploaded files. Run it on a trusted network — never expose it to the public internet.

Recodarr can offload encodes to a second instance of itself running on another host — typically a box with a much beefier GPU than wherever your media library lives. The same binary, same image, started with `RECODARR_MODE=agent`, becomes a stripped-down HTTP service that accepts encode jobs over the network, runs HandBrake locally, and streams the result back. The main Recodarr commits the result in place as if it had encoded locally, so the dashboard, progress, cancel, retry, and *arr refresh all keep working unchanged.

When to use it: your storage server is a low-power NAS, but you have a workstation with an Arc or RTX card sitting idle. When not to: everything's on one host (just bind-mount the media), or your network link is so slow that transfer dominates over encode time.

**Setup in one go** — on the GPU host:

```yaml
# docker-compose.agent.yml
services:
  recodarr-agent:
    image: ghcr.io/sratabix/recodarr:latest
    environment:
      RECODARR_MODE: agent
      RECODARR_AGENT_TOKEN: ${AGENT_TOKEN:?set this}
    volumes:
      - ./agent-data:/data           # no media mount needed
    ports:
      - "8090:8090"
    # Add the GPU passthrough block for your vendor — see GPU guides above.
```

Generate the token once with `openssl rand -hex 32` and put it in a sibling `.env`. Then in Recodarr's UI: **Settings → Remote Agent**, paste the URL (`http://gpu-host:8090`) and token, toggle **Use remote agent**, save, hit **Test connection**. Once the dashboard's health pill is green, every encode is dispatched to the agent.

If the agent goes offline, Recodarr falls back to local encoding by default (configurable). A failed network upload retries up to 5 times like any other job.

Full reference — protocol, failure modes, bandwidth math, security caveats, all `RECODARR_AGENT_*` env vars — lives in [`docs/remote-agent.md`](docs/remote-agent.md).

## Image tags

`latest` for the latest stable release. `1`, `1.2`, `1.2.3` to pin to a major, minor, or patch line. Pre-releases like `1.2.3-rc1` are never tagged `latest`. `dev` tracks the tip of the `main` branch (rebuilt on every commit) and is the easiest tag to use for testing without waiting for a release. Per-commit immutable tags are also published as `dev-<sha>`. Images are published to `ghcr.io/sratabix/recodarr` and built for `linux/amd64`.

## Environment variables

Everything else (qBit, *arr, profiles, mappings, window, etc.) lives in SQLite — set it in the UI.

| Var | Default | Purpose |
| --- | --- | --- |
| `RECODARR_DATA_DIR` | `/data` | DB, sessions, and `logs/` live here. Mount as a volume. |
| `RECODARR_ADDR` | `:8080` | HTTP listen address. |
| `TZ` | container default | Standard tz name (e.g. `Europe/Amsterdam`). Affects log timestamps and the encoding-window check. |
| `RECODARR_METRICS_TOKEN` | unset | If set, `/metrics` requires `Authorization: Bearer <token>`. |
| `RECODARR_TRUST_PROXY` | unset | Set to `1` only when behind a reverse proxy you control — enables `X-Forwarded-For` for per-IP login throttling. **Never set on a directly-exposed deployment.** |
| `RECODARR_MODE` | `server` | Set to `agent` to run the same binary as a remote encode worker. See [Remote encode agent](#remote-encode-agent) for the full feature; [`docs/remote-agent.md`](docs/remote-agent.md) covers the `RECODARR_AGENT_*` knobs. |

## CLI

```
recodarr               start the server
recodarr reset-admin   wipe the admin user, then visit the app to set up again
recodarr help          show usage
```

## License

[GPL-2.0](LICENSE).
