# REST API

Base URL: wherever `HTTP_ADDR` serves (default `http://localhost:8080`). All API routes are under `/api`; the same paths without `/api` serve the HTML web UI. Responses are JSON. List endpoints paginate with `cursor` and `limit`.

> This documents the intended MVP surface. The authoritative reference is your build's README and code — if a request here is rejected, check there and treat the code as truth.

## Health & stats

**Example:** Check system health and ingest statistics:

```bash
curl http://localhost:8080/health
curl http://localhost:8080/api/stats
```

Sample response payload for `GET /api/stats`:

```json
{
  "total_events": 14200,
  "contract_count": 85,
  "first_ledger": 500000,
  "last_ledger": 512000,
  "approximate": false
}
```

### `GET /health`
Reports process health, including the active source (RPC or SoroTrail) and, in standalone mode, database reachability.

### `GET /api/stats`
Overview counters: total contracts seen, total events, last-updated ledger (standalone mode) or last-synced position (upstream mode).

## Contracts

### `GET /api/contracts`
List every known contract, newest-activity-first.

**Query parameters:** `search` (partial contract ID match), `limit`, `cursor`.

```bash
curl 'http://localhost:8080/api/contracts?limit=20'
```

### `GET /api/contracts/{id}`
Summary for one contract: event count, first/last seen ledger, event-type breakdown.

## Events

### `GET /api/contracts/{id}/events`
A contract's events, newest-first.

**Query parameters:**

| Param | Description |
|---|---|
| `type` | `contract`, `system`, or `diagnostic` |
| `tx_hash` | Restrict to events from one transaction |
| `topic` | Match by decoded topic (see build semantics) |
| `from_ledger` / `to_ledger` | Inclusive ledger bounds |
| `limit` / `cursor` | Pagination |

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
- **Upstream mode (`SOURCE_MODE=sorotrail`):** Buckets are derived from the bounded recent-event scan and report `"approximate": true` whenever the scan does not exhaust the upstream history.

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
# Every event, optionally narrowed with contract_id
curl 'http://localhost:8080/api/events/buckets?from=2026-08-02T00:00:00Z&to=2026-08-02T03:00:00Z&interval=1h&type=contract'

# One contract's events; the contract ID comes from the path
curl 'http://localhost:8080/api/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC/buckets?from=2026-08-02T00:00:00Z&to=2026-08-02T03:00:00Z&interval=1h'
```

Both endpoints return the same `BucketSeries` shape. Each bucket is the number of matching events whose `ledger_closed_at` fell in that half-open slice, so the counts sum to `total_events` across the requested range. Quiet hours are present with `count: 0`, and `approximate` is `false` when the counts come from an exact standalone-mode aggregate.

```json
{
  "interval": "1h",
  "from": "2026-08-02T00:00:00Z",
  "to": "2026-08-02T03:00:00Z",
  "total_events": 42,
  "buckets": [
    { "start": "2026-08-02T00:00:00Z", "end": "2026-08-02T01:00:00Z", "count": 17 },
    { "start": "2026-08-02T01:00:00Z", "end": "2026-08-02T02:00:00Z", "count": 0 },
    { "start": "2026-08-02T02:00:00Z", "end": "2026-08-02T03:00:00Z", "count": 25 }
  ],
  "approximate": false
}
```

## Pagination

List responses include `next_cursor`; pass it back as `cursor` for the next page. An absent `next_cursor` means you've reached the end. Treat the cursor as opaque — its contents differ between standalone and upstream mode.

`limit` defaults to `50` and accepts `1`–`200`; values outside that range are rejected with `400` rather than clamped.

```bash
# First page: the two newest events
curl 'http://localhost:8080/api/events?limit=2'
```

Event objects are trimmed below to their paging-relevant fields; each is returned in full by the API.

```json
{
  "events": [
    { "id": "0016880389704384512-0000000003", "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC", "ledger": 3930272, "type": "contract" },
    { "id": "0016880389704384512-0000000002", "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC", "ledger": 3930272, "type": "contract" }
  ],
  "next_cursor": "0016880389704384512-0000000002"
}
```

Request the next page by passing that value back unchanged:

```bash
curl 'http://localhost:8080/api/events?limit=2&cursor=0016880389704384512-0000000002'
```

The same mechanism works on `GET /api/contracts` and `GET /api/contracts/{id}/events`; repeat until the response has no `next_cursor` field.

## Errors

Errors return an envelope with an HTTP status matching the failure class — `400` for invalid input (naming the offending parameter), `404` for an unknown contract or event, `500` for server faults. In upstream mode, an unreachable SoroTrail instance surfaces as a `502`-class error rather than a silent empty result.

The body carries a human-readable `error`, a stable `code` (the HTTP status text) and the request's `request_id`, which is also returned in the `X-Request-ID` header and logged server-side:

```bash
curl -i 'http://localhost:8080/api/contracts/not-a-contract/events'
```

```http
HTTP/1.1 400 Bad Request
Content-Type: application/json
X-Request-ID: 3f9c1a7d2b8e4c60

{
  "error": "invalid contract ID \"not-a-contract\"",
  "code": "Bad Request",
  "request_id": "3f9c1a7d2b8e4c60"
}
```

Branch on the status code (or `code`), show `error` to the user, and quote `request_id` in bug reports so the failure can be found in the logs. A `500` response deliberately returns a generic message such as `listing events failed` and keeps the underlying detail in the log only.
