# Deployment

## Docker Compose (single host, standalone mode)

```bash
cp .env.example .env    # set RPC_URL, DATABASE credentials
docker compose up -d
```

Put a reverse proxy (Caddy, nginx, Traefik) in front for TLS. SoroVista is read-only, so exposing it publicly is lower-risk than a service with a mutation API — but it's still worth deciding deliberately whether it should be public or internal-only.

If you would rather run the binary directly than in a container, [Deploying with systemd](systemd.md) has a complete unit file, an unprivileged user, and the hardening directives explained.

## Deploying in upstream mode

No local Postgres is needed — just the SoroVista binary/container and network access to your SoroTrail instance's API:

```bash
SOURCE_MODE=sorotrail
SOROTRAIL_URL=https://your-sorotrail-instance.example.com
HTTP_ADDR=:8080
```

This is the lighter deployment: one process, no database, no ingestion loop of its own. If you already run SoroTrail in production, running SoroVista alongside it in upstream mode is usually the simpler choice.

## Standalone mode operational notes

If you do run standalone mode, the same continuity consideration applies as any RPC-polling ingester: downtime beyond the RPC's retention window means a permanent gap in what SoroVista captured for that period. If continuous, gap-free history matters to you, prefer upstream mode against a SoroTrail instance that's already handling that concern.

### Bounding storage with `EVENT_RETENTION`

By default standalone mode keeps every event forever. Set `EVENT_RETENTION` to a duration to delete events whose on-chain `ledger_closed_at` is older than that horizon:

```bash
# Keep roughly 90 days of on-chain history and delete the rest.
EVENT_RETENTION=2160h
```

The sweeper runs hourly in standalone mode, deletes in bounded batches so a first pass cannot lock the table for a long transaction, and never touches `ingest_state` — a restart still resumes from where ingestion stopped. Each sweep logs the number of rows removed, and the `sorovista_events_pruned_total` counter is exposed on `/metrics`.

This is destructive and irreversible. Decide the horizon before enabling it, back up first, and prefer upstream mode if you need durable long history. `EVENT_RETENTION` is not related to `RETENTION_LEDGERS`: the latter only sets the cold-start reach-back for a fresh ingester and never deletes anything. Upstream mode has no local database and ignores both.

## Sizing

**Standalone mode:** light on compute; Postgres storage scales with how many contracts you watch and how long you run. **Upstream mode:** lighter still — no local storage growth at all, since SoroVista holds nothing beyond what it needs to render the current request.

## Upgrades

1. (Standalone only) back up the database.
2. Pull the new version, run migrations if standalone, restart.
3. Verify `GET /health` reports the expected source mode and reachability.
