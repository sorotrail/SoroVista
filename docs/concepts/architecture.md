# Architecture

SoroVista separates *where events come from* from *how they're shown*, so the UI and API don't care which mode is active.

```
                     ┌───────────────────────────────────────┐
                     │                SoroVista                │
                     │                                          │
 Stellar RPC ───┐    │  EventSource ──▶ Decoder ──▶ (Store)    │
 (standalone)   ├───▶│      │                          │        │
 SoroTrail API ─┘    │      └──────────┬───────────────┘        │
                     │                 ▼                        │
                     │        JSON API (chi) + htmx Web UI      │
                     └───────────────────────────────────────┘
```

## The EventSource abstraction

This is the design choice that defines SoroVista. `internal/source` declares an `EventSource` interface with two implementations:

- **`rpc`** — polls Stellar RPC's `getEvents` directly, the same approach as a lightweight indexer. Used in standalone mode.
- **`sorotrail`** — reads from a running SoroTrail instance's HTTP API instead, both for catch-up queries and (where available) live updates.

Everything above this interface — decoding, the API, the web UI — is written against `EventSource` and doesn't know or care which implementation is active. Adding a third source (a different indexer, a different network) means implementing the interface, not touching the rest of the app.

## Components

**Decoder** (`internal/decode`) — turns raw XDR ScVals into structured, readable JSON. Behind an interface so decoding quality (e.g. recognizing standard event shapes) can improve independently of everything else.

**Store** (`internal/store`) — Postgres persistence, used only in standalone mode. In upstream mode, SoroVista has no local event storage; every query is served live from the SoroTrail instance.

**API** (`internal/api`) — chi handlers exposing contracts, events, and stats as JSON, mirroring the web UI's data.

**Web** (`internal/web`) — server-rendered html/template + htmx pages: contracts list, contract detail, event detail, search. No frontend build step.

**RPC client** (`internal/rpc`) — wraps the Stellar RPC methods behind an interface, used by the `rpc` EventSource implementation.

**Entry point** (`cmd/sorovista`) — wires config, the selected EventSource, the store (if standalone), the API, and the web UI, with graceful shutdown.

## Why two modes instead of one

A contract explorer's whole value is what it can show you — and what it can show depends entirely on how much history is behind it. Standalone mode is simplest (nothing else to run) but only sees events since it started, bounded by the RPC's short retention. Upstream mode trades that simplicity for depth: if a SoroTrail instance has been running for months, SoroVista reading from it can show months of history on day one.

**Example:** switching modes is a single environment variable — no code changes, no recompile.

```bash
# Standalone mode: SoroVista polls Stellar RPC and stores events itself.
# Start here when you have no existing SoroTrail instance.
SOURCE_MODE=rpc
RPC_URL=https://soroban-testnet.stellar.org
DATABASE_URL=postgres://sorovista:sorovista@localhost:5432/sorovista?sslmode=disable
```

```bash
# Upstream mode: SoroVista reads from a running SoroTrail instance.
# Start here when SoroTrail already has months of indexed history.
SOURCE_MODE=sorotrail
SOROTRAIL_URL=http://sorotrail.internal:8080
```

The `/readyz` endpoint reports the active mode, so you can confirm which one is running:

```bash
curl -s http://localhost:8080/readyz
```

```json
{"status":true,"mode":"rpc","latest_ledger":3947332}
```

```json
{"status":true,"mode":"sorotrail","latest_ledger":3947332}
```

## Read-only by design

SoroVista never writes to a contract, never holds keys, and (beyond its own optional Postgres cache in standalone mode) never mutates anything. Its entire job is reading and presenting — which keeps its security surface small and its hosting requirements light.

Every request is a plain `GET` with no credentials:

```bash
curl -s http://localhost:8080/api/events/0016880389704384512-0000000003
```

```json
{
  "id": "0016880389704384512-0000000003",
  "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
  "type": "contract",
  "topics": [
    {"symbol": "transfer"},
    {"address": "GDS2XSFBG5KQ3G3UNGSA6EX6E4OS3CSBS3NHFS7AGWZP67KD7T46HQJH"}
  ],
  "value": {"i128": "1000"}
}
```

There is no write route to call — anything other than `GET` on these paths returns `405 Method Not Allowed`, so there is no key or token to leak.

## Observability (`internal/metrics`, `internal/reqid`, `internal/buildinfo`)

- `/metrics` — Prometheus instrumentation; the [metrics reference](../reference/metrics.md)
  lists every metric, its labels and example queries.
- `/livez`, `/readyz` — liveness checks nothing (restart loops otherwise);
  readiness consults the event source's `Status`, so it means the same
  thing regardless of backend.
- `/api/version` — version, commit and build date via `-ldflags`.
- Every request carries an `X-Request-ID`; error bodies and log lines echo
  it, so a reported error maps to one request in the logs.

```bash
# Build identity baked in at compile time
curl -s http://localhost:8080/api/version
```

```json
{"version":"v1.2.3","commit":"a1b2c3d","build_date":"2026-11-20T10:00:00Z"}
```

```bash
# Readiness (200 when the backend is reachable, 503 when it is not)
curl -s http://localhost:8080/readyz
```

```json
{"status":true,"mode":"rpc","latest_ledger":3947332}
```

```bash
# The correlation ID is on the response and echoed in the error body
curl -si http://localhost:8080/api/events/does-not-exist | grep -i x-request-id
# X-Request-Id: 4b1d0e2f9a7c3e51
```

Standalone mode verifies the RPC's network passphrase against the
configured one at startup and refuses to start on mismatch — see
[Configuration](../getting-started/configuration.md).
