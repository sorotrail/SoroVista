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

## Kubernetes (Helm)

The chart lives at `deploy/helm/sorovista`. It deploys one of the two modes,
selected by `sourceMode`, and omits the settings that do not apply to the
selected mode rather than emitting empty environment variables.

The repository has no release pipeline yet, so no image is published under the
default repository. Until it is, override `image.repository` and `image.tag`
with an image you have built from this repository:

```bash
docker build -t registry.example.com/sorovista:0.1.0 .
docker push registry.example.com/sorovista:0.1.0
```

### Standalone mode (`sourceMode: rpc`)

Standalone mode needs a Postgres database. `DATABASE_URL` can carry a
password, so the chart puts it in a Kubernetes Secret — never the ConfigMap.
Either let the chart create the Secret from `database.url`:

```bash
helm install sorovista deploy/helm/sorovista \
  --namespace sorovista --create-namespace \
  --set image.repository=registry.example.com/sorovista \
  --set image.tag=0.1.0 \
  --set database.url='postgres://user:password@postgres:5432/sorovista?sslmode=disable'
```

or reference a Secret you manage yourself:

```bash
kubectl -n sorovista create secret generic sorovista-db \
  --from-literal=dsn='postgres://user:password@postgres:5432/sorovista?sslmode=disable'

helm install sorovista deploy/helm/sorovista \
  --namespace sorovista \
  --set image.repository=registry.example.com/sorovista \
  --set image.tag=0.1.0 \
  --set database.existingSecret=sorovista-db \
  --set database.existingSecretKey=dsn
```

Migrations run automatically at startup, inside the process. With the default
`replicaCount: 1` that is safe. Every additional rpc-mode replica also starts
its own ingester and races the migration: `UpsertEvents` is idempotent, so data
is not corrupted, but RPC load doubles for no benefit. Keep rpc mode at one
replica unless you have moved ingestion and migrations to a single writer.

### Upstream mode (`sourceMode: sorotrail`)

Upstream mode is stateless, needs no database, and can be scaled freely:

```bash
helm install sorovista deploy/helm/sorovista \
  --namespace sorovista --create-namespace \
  --set image.repository=registry.example.com/sorovista \
  --set image.tag=0.1.0 \
  --set sourceMode=sorotrail \
  --set sorotrailUrl=http://sorotrail.monitoring.svc:8080 \
  --set replicaCount=2
```

### Probes

`livenessProbe` calls `/livez` and `readinessProbe` calls `/readyz`, preserving
their documented and deliberately different meanings: liveness checks nothing
so a dead backend cannot trigger a restart loop, while readiness consults the
event source and removes the pod from service until it can answer. The chart
also sets a `startupProbe` on `/livez` so slow migrations do not trip liveness.

### Configuration values

Every environment variable `internal/config` reads is settable through the
chart. A test under `deploy/helm/sorovista/test` fails when a variable is added
to the config without a corresponding chart mapping.

| Value | Environment variable | Mode | Default |
| --- | --- | --- | --- |
| `sourceMode` | `SOURCE_MODE` | both | `rpc` |
| `httpAddr` | `HTTP_ADDR` | both | `:8080` |
| `logLevel` | `LOG_LEVEL` | both | `info` |
| `corsAllowedOrigins` | `CORS_ALLOWED_ORIGINS` | both | empty (omitted) |
| `network` | `NETWORK` | rpc | `testnet` |
| `rpcUrl` | `RPC_URL` | rpc | empty (network preset) |
| `networkPassphrase` | `NETWORK_PASSPHRASE` | rpc | empty (network preset) |
| `pollInterval` | `POLL_INTERVAL` | rpc | `5s` |
| `watchedContracts` | `WATCHED_CONTRACTS` | rpc | empty (omitted) |
| `startLedger` | `START_LEDGER` | rpc | empty (omitted) |
| `retentionLedgers` | `RETENTION_LEDGERS` | rpc | `17280` |
| `database.url` / `database.existingSecret` | `DATABASE_URL` | rpc | empty |
| `sorotrailUrl` | `SOROTRAIL_URL` | sorotrail | empty |

Optional features:

- `serviceMonitor.enabled` (default `false`) creates a Prometheus Operator
  `ServiceMonitor` for `/metrics`. It needs the `monitoring.coreos.com/v1`
  CRDs; add your Prometheus selection labels under `serviceMonitor.labels`.
- `podDisruptionBudget.enabled` (default `false`) creates a
  `policy/v1` PDB with `minAvailable`. With the default single replica a
  `minAvailable: 1` PDB blocks voluntary evictions, so enable it only when
  running more than one replica.
- `resources` accepts the usual `requests`/`limits` block and is empty by
  default.

### Working on the chart

```bash
helm lint deploy/helm/sorovista
go test ./deploy/helm/sorovista/test -v
```

The render tests skip when the `helm` binary is absent. CI has a dedicated Helm
job that installs Helm, runs `helm lint` and runs those tests, so a missing
binary fails that job instead of showing up as a silent pass. After an
intentional template change, regenerate the golden files and review the diff:

```bash
go test ./deploy/helm/sorovista/test -run TestGoldenRender -update
```

## Standalone mode operational notes

If you do run standalone mode, the same continuity consideration applies as any RPC-polling ingester: downtime beyond the RPC's retention window means a permanent gap in what SoroVista captured for that period. If continuous, gap-free history matters to you, prefer upstream mode against a SoroTrail instance that's already handling that concern.

## Sizing

**Standalone mode:** light on compute; Postgres storage scales with how many contracts you watch and how long you run. **Upstream mode:** lighter still — no local storage growth at all, since SoroVista holds nothing beyond what it needs to render the current request.

## Upgrades

1. (Standalone only) back up the database.
2. Pull the new version, run migrations if standalone, restart.
3. Verify `GET /health` reports the expected source mode and reachability.
