# Deployment

## Docker Compose (single host, standalone mode)

```bash
cp .env.example .env    # set RPC_URL, DATABASE credentials
docker compose up -d
```

Put a reverse proxy (Caddy, nginx, Traefik) in front for TLS. SoroVista is read-only, so exposing it publicly is lower-risk than a service with a mutation API — but it's still worth deciding deliberately whether it should be public or internal-only.

**Example:** Check container status and verify the service is running:

```bash
docker compose ps
curl http://localhost:8080/health
```

## Deploying in upstream mode

No local Postgres is needed — just the SoroVista binary/container and network access to your SoroTrail instance's API:

```bash
SOURCE_MODE=sorotrail
SOROTRAIL_URL=https://your-sorotrail-instance.example.com
HTTP_ADDR=:8080
```

**Example:** Run SoroVista directly in upstream mode:

```bash
SOURCE_MODE=sorotrail \
SOROTRAIL_URL=https://your-sorotrail-instance.example.com \
HTTP_ADDR=:8080 \
./bin/sorovista
```

This is the lighter deployment: one process, no database, no ingestion loop of its own. If you already run SoroTrail in production, running SoroVista alongside it in upstream mode is usually the simpler choice.

## Standalone mode operational notes

If you do run standalone mode, the same continuity consideration applies as any RPC-polling ingester: downtime beyond the RPC's retention window means a permanent gap in what SoroVista captured for that period. If continuous, gap-free history matters to you, prefer upstream mode against a SoroTrail instance that's already handling that concern.

**Example:** Query `/api/stats` to verify the ingested ledger range and check for continuity:

```bash
curl http://localhost:8080/api/stats
```

Sample response payload:

```json
{
  "total_events": 14200,
  "contract_count": 85,
  "first_ledger": 500000,
  "last_ledger": 512000,
  "approximate": false
}
```

## Sizing

**Standalone mode:** light on compute; Postgres storage scales with how many contracts you watch and how long you run. **Upstream mode:** lighter still — no local storage growth at all, since SoroVista holds nothing beyond what it needs to render the current request.

**Example:** Container resource limits specification in `docker-compose.yml`:

```yaml
services:
  sorovista:
    deploy:
      resources:
        limits:
          cpus: '0.50'
          memory: 512M
        reservations:
          cpus: '0.10'
          memory: 128M
```

## Upgrades

1. (Standalone only) back up the database.
2. Pull the new version, run migrations if standalone, restart.
3. Verify `GET /health` reports the expected source mode and reachability.

**Example:** Upgrade a Docker Compose deployment:

```bash
# 1. Back up database (standalone mode)
docker compose exec postgres pg_dump -U sorovista sorovista > backup_$(date +%Y%m%d).sql

# 2. Pull latest images and restart containers
docker compose pull
docker compose up -d

# 3. Verify health
curl http://localhost:8080/health
```
