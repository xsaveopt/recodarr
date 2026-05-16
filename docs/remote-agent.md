# Remote encode agent

Run a second `recodarr` container in **agent mode** on a host with a beefier GPU. The main Recodarr ships encode jobs to the agent over HTTP, the agent runs HandBrake locally, and streams the result back to be committed in place. Same image, mode chosen by env var.

## Quick start

On the GPU host:

```yaml
# docker-compose.agent.yml
services:
  recodarr-agent:
    image: ghcr.io/sratabix/recodarr:latest
    container_name: recodarr-agent
    restart: unless-stopped
    environment:
      RECODARR_MODE: agent
      RECODARR_AGENT_TOKEN: ${AGENT_TOKEN:?set this}
      RECODARR_AGENT_MAX_PARALLEL: "1"
    volumes:
      - ./agent-data:/data            # NO media mount needed — files travel over HTTP
    ports:
      - "8090:8090"
    deploy:                            # Nvidia example; see GPU docs below
      resources:
        reservations:
          devices:
            - capabilities: [gpu]
```

Generate the token once:

```bash
openssl rand -hex 32
```

Then in Recodarr's UI: **Settings → Remote Agent** → paste the URL (`http://gpu-host:8090`) and token, toggle **Use remote agent** on, save, then **Test connection**. The dashboard's health pill shows `Healthy` when the next probe succeeds.

## Environment variables

| Var | Required | Default | Purpose |
| --- | --- | --- | --- |
| `RECODARR_MODE` | yes | `server` | Set to `agent` to enter this mode. |
| `RECODARR_AGENT_TOKEN` | yes | — | Shared bearer secret. Mismatch → 401 on every request. |
| `RECODARR_AGENT_ADDR` | no | `:8090` | HTTP listen address. |
| `RECODARR_DATA_DIR` | no | `/data` | Per-job state, uploads, and outputs go under `<dir>/agent/jobs/<id>/`. |
| `RECODARR_AGENT_MAX_PARALLEL` | no | `1` | Concurrent encodes. HandBrake hwaccel doesn't parallelize on consumer GPUs; raise only on workstation cards. |
| `RECODARR_AGENT_LOG_LEVEL` | no | `INFO` | `DEBUG` / `INFO` / `WARN` / `ERROR` for the agent's stdout. |

The agent has **no database, no SPA, no Sonarr/Radarr clients, no encoding queue from webhooks** — only the HTTP protocol below.

## What the agent does and doesn't do

It does:

- Accept a job (`POST /v1/jobs`) carrying the HandBrake `Settings` blob the server-side worker would have used locally.
- Receive the source file (`PUT /v1/jobs/{id}/source`).
- Run HandBrake against the uploaded file with the requested settings.
- Stream progress + state transitions over SSE (`GET /v1/jobs/{id}/events`).
- Serve the encoded result for download (`GET /v1/jobs/{id}/output`, with HTTP Range support).
- Clean up its working directory on `DELETE /v1/jobs/{id}` (and after an idle TTL if the server-side client forgot).

It doesn't:

- Speak to Sonarr/Radarr — the main Recodarr does that.
- Persist anything across reboots in a database — state is one `state.json` per job dir.
- Authenticate per-user or per-job — one shared token for the whole agent.
- Terminate TLS — put Caddy/Traefik/Nginx in front for HTTPS.
- Resume partial uploads — a failed `PUT` retries from byte 0. Recodarr's job retry semantics (5 attempts) cover this.

## Bandwidth and when it's worth it

Two transfers per encode: source up, result down. On gigabit LAN that's ~1 GB/min each way. For a 12 GB Bluray rip: ~12 min upload + encode time + ~3 min download. Worth it when:

- The GPU host can encode faster than gigabit transfer time + your CPU's local encode rate combined.
- You have multiple Recodarr instances feeding one beefy encoder.
- You want to keep your storage host quiet (no HandBrake at 100% GPU there).

Not worth it for:

- Smaller files (<2 GB) where transfer dominates over encode.
- Slow links (<100 Mbps).
- Single-host setups where you'd just bind-mount the media directly.

## Failure modes

| What | What happens |
| --- | --- |
| Agent down | Health check fires red. If `Fall back to local` is on (default), encodes run locally until the next successful probe rebinds. |
| Network drops during upload | Recodarr's job goes `failed`, retried up to 5 times. Each retry uploads from scratch. |
| Network drops during result download | HTTP Range is supported; in v1 the client doesn't resume, but a retry of the whole job restarts. |
| Agent crashes mid-encode | On restart, the in-flight `state.json` is rewritten to `failed` ("agent restarted") so the server-side poll sees a definite terminal state. |
| Output disk fills on agent | Encode fails with HandBrake's error verbatim. The job dir is left in place for inspection; clean it manually or let the 1h TTL handle it. |

## Security

The agent's protocol is **one bearer token, no per-job auth, no TLS**. Treat the agent like an internal service:

- Run it on a network your Recodarr already trusts (LAN, WireGuard mesh, etc.).
- If you must expose it across a hostile network, terminate TLS at a reverse proxy and require the proxy to forward `Authorization` unchanged.
- Anyone with the token can submit arbitrary HandBrake settings and read back uploads. Rotate by changing the env var and restarting; update Recodarr's stored token in **Settings → Remote Agent**.

`GET /v1/healthz` is intentionally unauthenticated so a reverse-proxy liveness probe doesn't need the token.

## GPU passthrough

Same setup as the main Recodarr container — see [`docs/gpu-nvidia.md`](gpu-nvidia.md) and [`docs/gpu-intel.md`](gpu-intel.md). The agent only needs the GPU; it doesn't need access to your media share.

## Protocol reference

All endpoints under `/v1`. Auth: `Authorization: Bearer <token>` on everything except `/healthz`.

| Method | Path | Purpose |
| --- | --- | --- |
| `POST` | `/v1/jobs` | Create a job. Body: `{filename, sizeBytes, settings, outputContainer}`. Returns `{jobId, uploadUrl}`. |
| `PUT` | `/v1/jobs/{id}/source` | Stream raw source bytes. `Content-Length` must match the declared `sizeBytes`. |
| `GET` | `/v1/jobs` | List current jobs. |
| `GET` | `/v1/jobs/{id}` | JSON snapshot of one job. |
| `GET` | `/v1/jobs/{id}/events` | SSE stream of `progress` + `state` events. |
| `GET` | `/v1/jobs/{id}/output` | Download encoded file. Range supported. |
| `GET` | `/v1/jobs/{id}/log` | Plain-text HandBrake stdout/stderr from the encode. |
| `DELETE` | `/v1/jobs/{id}` | Cancel if active, delete on disk. Idempotent. |
| `GET` | `/v1/healthz` | Unauthed. `{version, handbrakeVersion, slotsUsed, slotsMax, jobsActive, diskFreeBytes}`. |

States: `awaiting_source → queued → encoding → done | failed | cancelled`.
