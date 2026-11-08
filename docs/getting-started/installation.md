# Installation

SoroVista is a single Go binary with a server-rendered web UI. Postgres is only required in standalone mode.

## Docker Compose (recommended)

```bash
git clone https://github.com/sorotrail/SoroVista.git
cd SoroVista
cp .env.example .env   # edit as needed
docker compose up -d
```

Runs standalone mode with its bundled Postgres by default. The UI and API are served on `HTTP_ADDR` (default `:8080`).

## From source

Requirements: Go 1.22+, and — for standalone mode — a reachable Postgres 14+ instance.

```bash
git clone https://github.com/sorotrail/SoroVista.git
cd SoroVista
make build          # or: go build ./...
```

**Standalone mode:**

```bash
make migrate
SOURCE_MODE=rpc \
RPC_URL=https://soroban-testnet.stellar.org \
DATABASE_URL=postgres://user:pass@localhost:5432/sorovista?sslmode=disable \
./sorovista
```

**Upstream mode** (reading from a SoroTrail instance — no local database needed):

```bash
SOURCE_MODE=sorotrail \
SOROTRAIL_URL=https://your-sorotrail-instance.example.com \
./sorovista
```

## Verifying the install

```bash
curl http://localhost:8080/health
```

If the configured source (RPC or SoroTrail URL) or database is unreachable, the health response says so.

## Upgrading

Pull the new version, run pending migrations (standalone mode only), restart. In upstream mode there's no local data to migrate — SoroVista holds no state of its own beyond what it queries live from SoroTrail.
