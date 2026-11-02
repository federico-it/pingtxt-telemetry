# pingtxt-telemetry

Anonymous usage stats for [pingtxt](https://github.com/federico-it/pingtxt):
how many self-hosted hubs are live, and how many monitors and agents they run.

Hubs send nothing until an admin says yes to the one-time question after
install (or turns it on in settings → general → usage stats). Then, once an hour, a hub
sends only this:

```json
{"id": "K7QZ…", "version": "v0.5.0", "monitors": 12, "agents": 3}
```

- `id`: random, made by the hub the first time stats are turned on, never changes.
- `version`: the pingtxt version.
- `monitors`: monitors not paused. `agents`: agents connected right now.

No names, targets, addresses, results or emails. The service does **not** store
IP addresses: the client IP is used only for an in-memory rate limit.
Only totals are public: no single hub's report is ever shown.
`PINGTXT_DISABLE_TELEMETRY=true` on a hub turns the stats off for good.
The hub side is described in the pingtxt docs, under
[usage stats](https://pingtxt.com/docs/administration/#usage-stats).

## What it keeps

- The last report of each hub (id, version, counts, first and last seen).
  Hubs that stop reporting are deleted after `TELEMETRY_KEEP_DAYS` (180).
- One row per day with the highest live totals of that day, for the chart.

A hub is **live** when its last report is newer than `TELEMETRY_LIVE_WINDOW`
(2h: hubs report every hour, plus a few minutes of jitter).

## API

| | |
|---|---|
| `POST /v1/report` | a hub's report (JSON above, max 4 KB). `204`, `400` invalid, `429` rate limited |
| `GET /v1/stats` | public, cached 1 minute, CORS open: live totals, versions of live hubs, hubs seen in 30 days, new in 7 days, 90 days of history |
| `GET /` | a public page with the numbers |
| `GET /healthz` | `{"status":"ok","version":"…"}`, or `503` with `"status":"error"` when the database does not answer |

[`docs/openapi.json`](docs/openapi.json) describes these routes (OpenAPI 3.0),
ready for Cloudflare API Shield schema validation.

## Run

```sh
docker compose up -d
```

| Variable | Default | |
|---|---|---|
| `TELEMETRY_DATA` | `/data` | folder of `telemetry.db` (SQLite). Mount a volume here |
| `TELEMETRY_ADDR` | `:8080` | listen address |
| `TELEMETRY_TRUST_PROXY` | `false` | `true` behind Traefik/Caddy/nginx: rate limit by `X-Forwarded-For` |
| `TELEMETRY_CLIENT_IP_HEADER` | | header with the client IP set by a CDN, checked first: `CF-Connecting-IP` behind Cloudflare (there the last `X-Forwarded-For` entry is a Cloudflare address). Only set it when the origin accepts traffic from the CDN alone |
| `TELEMETRY_LIVE_WINDOW` | `2h` | how recent a report must be to count as live |
| `TELEMETRY_KEEP_DAYS` | `180` | delete hubs not seen for this many days |
| `TELEMETRY_IP_RATE` | `30` | reports per IP per minute (0 = no limit) |
| `TELEMETRY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

### Coolify

New resource from the GitHub repository → build pack **Dockerfile**, port
`8080`, domain `https://telemetry.pingtxt.com` (the hub's default). Add a
persistent storage volume on `/data`, and set `TELEMETRY_TRUST_PROXY=true`
(behind Cloudflare also `TELEMETRY_CLIENT_IP_HEADER=CF-Connecting-IP`).
The image has no shell or curl: turn the Coolify health check off (the Docker `HEALTHCHECK` runs `/pingtxt-telemetry healthcheck`).

## Build

One static Go binary (SQLite through the pure Go `modernc.org/sqlite`) on a
`scratch` image, running as uid 10001.

```sh
go test ./...
docker build -t pingtxt-telemetry .
```

## Contributing and security

See [CONTRIBUTING.md](CONTRIBUTING.md). Report vulnerabilities privately as described in
[SECURITY.md](SECURITY.md), never in a public issue.

## License

AGPL-3.0, like pingtxt.
