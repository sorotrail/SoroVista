# Deployment

## Docker Compose (single host, standalone mode)

```bash
cp .env.example .env    # set RPC_URL, DATABASE credentials
docker compose up -d
```

Put a reverse proxy (Caddy, nginx, Traefik) in front for TLS. SoroVista is read-only, so exposing it publicly is lower-risk than a service with a mutation API — but it's still worth deciding deliberately whether it should be public or internal-only.

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

## Sizing

**Standalone mode:** light on compute; Postgres storage scales with how many contracts you watch and how long you run. **Upstream mode:** lighter still — no local storage growth at all, since SoroVista holds nothing beyond what it needs to render the current request.

## Health and readiness

`GET /health` returns `200` when every dependency is healthy and `503` otherwise. `GET /readyz` returns the same verdict for an orchestrator, and also includes a `dependencies` array so an alert can distinguish component failures:

```json
{
  "mode": "rpc",
  "healthy": false,
  "status": "not ready",
  "detail": "database unreachable: …",
  "dependencies": [
    {"name": "postgres", "healthy": false, "severity": "fatal", "detail": "database unreachable: …"},
    {"name": "stellar_rpc", "healthy": true, "severity": "degraded"}
  ]
}
```

Each dependency has a `severity` describing what its failure means, not whether it is currently failing:

- **`fatal`** — losing it breaks the read paths. Standalone mode's `postgres` and upstream mode's `sorotrail` are fatal. Alert this as a full outage; the instance can serve nothing useful.
- **`degraded`** — losing it leaves stored data queryable. Standalone mode's `stellar_rpc` is degraded: an unreachable node stops ingestion but every event already stored is still browsable. Alert this as stalled ingestion, not total failure.

Which dependencies appear is mode-specific: upstream mode opens no database, so it never reports `postgres`. Each check is bounded by a short timeout and is cheap enough for probe frequency (a database ping, not a query). Detail strings are sanitized and never contain credentials. Liveness (`/livez`) intentionally checks nothing, so a dependency outage does not restart-loop a recoverable instance.

## Tracing

OpenTelemetry tracing is off by default. Enable it with:

```bash
OTEL_ENABLED=true
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318   # OTLP/HTTP, scheme optional
OTEL_SAMPLE_RATE=0.1                                  # 0..1, default 0.1
OTEL_SERVICE_NAME=sorovista
```

Spans cover the inbound HTTP request, outbound Stellar RPC and SoroTrail calls, each store query and each ingest poll cycle. W3C `traceparent` is extracted from inbound requests and injected into outbound ones, so a caller's trace continues through SoroVista. The request ID is attached to the request span as `request_id`, joining a trace to the matching log line. Span names stay bounded: the HTTP server span has a fixed name rather than using the raw path.

A minimal local collector:

```bash
docker run --rm -p 4318:4318 otel/opentelemetry-collector:latest
```

A slow or unreachable collector does not fail requests and is logged once, not per span. Shutdown flushes pending spans within the process's existing shutdown timeout, so tracing can never block pod termination.

## Upgrades

1. (Standalone only) back up the database.
2. Pull the new version, run migrations if standalone, restart.
3. Verify `GET /health` reports the expected source mode and reachability.
