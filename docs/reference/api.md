# REST API

Base URL: wherever `HTTP_ADDR` serves (default `http://localhost:8080`). All API routes are under `/api`; the same paths without `/api` serve the HTML web UI. Responses are JSON. List endpoints paginate with `cursor` and `limit`.

> This documents the intended MVP surface. The authoritative reference is your build's README and code — if a request here is rejected, check there and treat the code as truth.

## Health & stats

### `GET /health`
Reports process health, including the active source (RPC or SoroTrail) and, in standalone mode, database reachability.

### `GET /api/stats`
Overview counters: total contracts seen, total events, `first_ledger`, `last_ledger`, and an event-type breakdown (`type_breakdown`).

- **`approximate` (`bool`):**
  - `false` in standalone mode (`SOURCE_MODE=rpc`), where all counters, ledger bounds, and `type_breakdown` are computed from exact SQL aggregates over the local Postgres `events` table.
  - `true` in upstream mode (`SOURCE_MODE=sorotrail`). While `total_events`, `contract_count`, and `last_ledger` come directly from the SoroTrail indexer's `/stats` endpoint and are exact, `type_breakdown` and `first_ledger` are derived from a bounded recent-event scan (up to `5,000` events). Because the struct mixes exact totals with a scan-derived breakdown, the response sets `"approximate": true` so clients and the UI label derived figures appropriately.
- **`first_ledger` and `last_ledger` (`0` means unknown):**
  - A value of `0` means the bound is **unknown** (or no events have been observed yet), not ledger zero. Upstream mode reports `first_ledger: 0` when the indexer has no events or when the range cannot be determined from the bounded scan.

## Contracts

### `GET /api/contracts`
List every known contract, newest-activity-first (`last_ledger` descending, then contract `id` ascending).

**Query parameters:** `search` (case-insensitive partial contract ID match), `limit` (`1..200`, default `50`), `cursor`.

- **Standalone mode (`SOURCE_MODE=rpc`):** Reads directly from the indexed Postgres `events` table.
- **Upstream mode (`SOURCE_MODE=sorotrail`):** Because SoroTrail does not expose a contracts endpoint, SoroVista derives the contract list by scanning up to `5,000` recent events backwards from the indexer's tip and caches the aggregated list in memory for `30s`. Contracts that have not emitted events within that bounded recent-event window will not appear in `GET /api/contracts` even if older events exist in SoroTrail.

```bash
curl 'http://localhost:8080/api/contracts?limit=20'
```

### `GET /api/contracts/{id}` and `GET /api/contracts/{id}/stats`
Summary for one contract: `contract_id`, `total_events`, `first_ledger`, `last_ledger`, `type_breakdown`, and `approximate`.

- **`approximate` (`bool`):** `false` in standalone mode (exact SQL count). In upstream mode, SoroVista scans up to `5,000` events for `{id}` backwards from the indexer tip: if that scan reaches the beginning of history, `approximate` is `false` (every event was counted); if the scan hits its budget before exhausting history, `approximate` is `true`.
- **`first_ledger` and `last_ledger` (`0` means unknown):** Both are `0` when no events are known for the contract within the visible/scanned window.

## Events

### `GET /api/events` and `GET /api/contracts/{id}/events`
Events across all contracts (`GET /api/events`) or for a single contract (`GET /api/contracts/{id}/events`), newest-first.

**Query parameters:**

| Param | Description |
|---|---|
| `type` | `contract`, `system`, or `diagnostic` |
| `tx_hash` | Restrict to events from one transaction |
| `topic` | Match by decoded topic (a bare word such as `transfer` matches `{"symbol":"transfer"}`, or pass any valid JSON value) |
| `from_ledger` / `to_ledger` | Inclusive ledger bounds (positive integers, `from_ledger <= to_ledger`) |
| `limit` / `cursor` | Pagination (`limit` in `1..200`, default `50`) |

```bash
curl 'http://localhost:8080/api/contracts/CC.../events?type=contract&limit=50'
```

### `GET /api/events/{id}`
Fetch a single event by its unique id (TOID), fully decoded.

### `GET /api/events/stream`
Stream newly ingested events in real time over Server-Sent Events (`text/event-stream`).

**Query parameters:** Accepts the same filters as `GET /api/events`: `contract_id`, `type` (`contract`, `system`, `diagnostic`), `topic`, `tx_hash`, `from_ledger`, and `to_ledger`.

```bash
curl -N 'http://localhost:8080/api/events/stream?contract_id=CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC&type=contract'
```

**Frame format & heartbeats:**
- An immediate `:hb` comment frame is flushed upon connection so clients receive HTTP 200 headers right away, followed by `:hb` comments every 15 seconds to keep idle connections alive through reverse proxies.
- Matching events are emitted as `event: event` frames with `id: <TOID>` and `data: <Event JSON>`.

**Backpressure & delivery guarantees:**
- Delivery is **best-effort** and live-only (no database connection is held per subscriber).
- Each subscriber has a bounded in-memory channel of **256 events**. The ingester's `Publish` path is non-blocking: if a slow or stalled client fills its 256-event buffer, the broker closes that subscriber's stream and removes the subscription rather than stalling ingestion or growing memory without bound.
- Clients that disconnect or are dropped for backpressure can catch up without gaps by querying `GET /api/events` with a `cursor` or `from_ledger`.
- **Upstream mode (`SOURCE_MODE=sorotrail`):** Because upstream mode has no local ingester, `GET /api/events/stream` explicitly returns `501 Not Implemented` rather than holding open a silent stream.

### `GET /api/events/buckets` and `GET /api/contracts/{id}/buckets`
Aggregate event counts into contiguous half-open `[start, end)` time buckets over a `[from, to)` RFC3339 timestamp range.

- **Timestamp bucketed:** `ledger_closed_at` (the on-chain ledger close time, not `created_at` ingest time, so backfilled history is placed at its true on-chain time).
- **Half-open boundaries:** Each bucket covers `[start, end)` (`start <= ledger_closed_at < end`). An event whose `ledger_closed_at` equals a bucket boundary lands in the bucket starting at that boundary; an event at `to` is excluded.
- **Empty buckets:** Quiet intervals within `[from, to)` are returned with `"count": 0` rather than omitted.
- **Bounds & cap:** `interval` defaults to `1h` and accepts durations between `1m` and `168h` (`7d`), such as `1m`, `5m`, `15m`, `1h`, `6h`, `1d`. A single request may produce at most `500` buckets (`400 Bad Request` when `(to - from) / interval > 500`).
- **Upstream mode (`SOURCE_MODE=sorotrail`):** Buckets are derived from the bounded recent-event scan and report `"approximate": true` whenever the scan does not exhaust the upstream history (and `"approximate": false` when the scan reaches the lower ledger bound or the indexer is empty).

**Query parameters:**

| Param | Description |
|---|---|
| `from` | Required inclusive lower bound (RFC3339 timestamp) |
| `to` | Required exclusive upper bound (RFC3339 timestamp) |
| `interval` | Bucket width (`1m` .. `7d`, default `1h`; max 500 buckets) |
| `contract_id` | Optional Soroban contract strkey (`GET /api/events/buckets` only) |
| `type` | Optional `contract`, `system`, or `diagnostic` |
| `topic` | Optional decoded topic filter (bare symbol or JSON object) |

```bash
curl 'http://localhost:8080/api/events/buckets?from=2024-01-01T00:00:00Z&to=2024-01-01T06:00:00Z&interval=1h&type=contract'
```

## Pagination

List endpoints (`GET /api/contracts`, `GET /api/events`, and `GET /api/contracts/{id}/events`) return up to `limit` items (`1..200`, default `50`) ordered newest-first alongside an optional `next_cursor` string. Pass `next_cursor` back as the `cursor` query parameter to fetch the following page; an absent or empty `next_cursor` indicates that no older results remain.

### Cursor contract

- **Opaque token:** Treat `cursor` and `next_cursor` as strictly opaque tokens. Do not parse, construct, or modify cursor strings; only the backend implementation that produced a cursor may interpret it.
- **Not portable across modes:** A cursor generated in standalone mode (`SOURCE_MODE=rpc`) cannot be used against an instance running in upstream mode (`SOURCE_MODE=sorotrail`), or vice versa. Clients that persist cursors across restarts or redeployments must discard them if `SOURCE_MODE` changes.
- **Not guaranteed stable across versions:** Cursor representations are an internal detail of each `source.EventSource` implementation and may change between SoroVista releases.
- **Contract list cache boundary in upstream mode:** In upstream mode, `GET /api/contracts` paginates over the cached recent-contract list (30-second TTL). If the cache refreshes between page requests while new contracts become active, page boundaries can shift relative to the previous snapshot.

### Paging cost in standalone vs upstream mode

The two backends execute pagination differently:

- **Standalone mode (`SOURCE_MODE=rpc`):** Cursors seek directly into an indexed Postgres query (`events` ordered by zero-padded TOID descending, or contract summaries ordered by `(last_ledger DESC, contract_id ASC)`). Every page costs a single indexed SQL query regardless of how selective the filters are or how far back in history the cursor sits.
- **Upstream mode (`SOURCE_MODE=sorotrail`):** SoroVista has no local database and reads from SoroTrail's HTTP API, which returns events in ascending order only. To serve newest-first pages, SoroVista walks adaptive ledger windows **backwards** from `to_ledger` (or the cursor's resume point) down toward `from_ledger` — starting with a 5,000-ledger window, narrowing by 4× when a window is dense (exceeding 8 upstream pages), and widening by 4× when a window is sparse, for up to 24 windows per request.
  - **Consequence for sparse filters:** Paging cost in upstream mode grows with how sparse the matching events are. A selective `topic`, `type`, or `tx_hash` filter that matches very few events across millions of ledgers will walk many ledger windows to fill one page, and may stop when the per-request window budget (24 windows) is reached. When the window budget is exhausted before `from_ledger` is reached, SoroVista returns the events collected so far along with a `next_cursor` that resumes below the oldest event seen.
  - **Practical advice for clients:** In upstream mode, always supply explicit `from_ledger` and `to_ledger` bounds when querying historical slices or selective filters, rather than relying on `topic` or `type` filters alone to narrow an unbounded scan across the entire chain history.

### Invalid or stale cursors

- On `GET /api/contracts` (both modes) and on `GET /api/events` / `GET /api/contracts/{id}/events` in upstream mode (`SOURCE_MODE=sorotrail`), passing a malformed cursor — or a cursor produced by the other operating mode — fails cursor decoding inside the source layer and currently returns **`500 Internal Server Error`** with the standard error envelope (`{"error":"listing events failed","code":"Internal Server Error","request_id":"..."}` or `"listing contracts failed"`).
- On `GET /api/events` and `GET /api/contracts/{id}/events` in standalone mode (`SOURCE_MODE=rpc`), `cursor` is compared lexicographically against the zero-padded `events.id` column in SQL (`id < $cursor ORDER BY id DESC`) without base64 decoding, so an arbitrary string returns **`200 OK`** with whatever events sort lexicographically below that token.
- If a client receives an error when resuming from a saved cursor, it should discard the saved cursor and restart pagination from the first page (without `cursor`).

## Errors

Errors return an envelope (`{"error": "...", "code": "...", "request_id": "..."}`) with an HTTP status matching the failure class — `400 Bad Request` for invalid query parameters (`limit`, `type`, `from_ledger`, `to_ledger`, `from`, `to`, `interval`, or `contract_id`), `404 Not Found` for an unknown event TOID, `501 Not Implemented` for `GET /api/events/stream` in upstream mode, and `500 Internal Server Error` for backend or cursor-decoding faults. In upstream mode, an unreachable SoroTrail instance surfaces as a `500` error on data endpoints (and `503 Service Unavailable` on `/health` and `/readyz`) rather than a silent empty result.
