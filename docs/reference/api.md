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
curl 'http://localhost:8080/api/events/buckets?from=2024-01-01T00:00:00Z&to=2024-01-01T06:00:00Z&interval=1h&type=contract'
```

## Export

### `GET /api/events/export` and `GET /api/contracts/{id}/events/export`
Stream matching events as CSV or NDJSON. The contract-scoped form fixes the contract; the global form accepts `contract_id` like the other event lists. Both accept the same filters as the JSON endpoints (`type`, `tx_hash`, `topic`, `from_ledger`, `to_ledger`, `cursor`), so an export selects exactly what a paged query would.

**Format selection:** `format=csv` (default) or `format=ndjson`. Format is a query parameter rather than an `Accept` header because every other API option is already a query parameter, and a URL is easier to paste into a browser, `curl` or a spreadsheet import than content negotiation.

```bash
# One contract's history, ready to open in a spreadsheet
curl -OJ 'http://localhost:8080/api/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC/events/export?format=csv'

# Every transfer on the network, streamed into jq
curl -sN 'http://localhost:8080/api/events/export?format=ndjson&topic=transfer' | jq -c '.id'
```

**Response:**
- CSV is `text/csv; charset=utf-8` with an `attachment` `Content-Disposition` filename, so a browser downloads rather than renders it. There is a header row.
- NDJSON is `application/x-ndjson`: one annotated event object per line, no enclosing array or commas. Each line has the same shape as a `GET /api/events` item (`token` included when detected).

**CSV columns (stable):**

| Column | Contents |
|---|---|
| `id` | Event TOID |
| `contract_id` | Soroban contract strkey |
| `ledger` | Ledger sequence |
| `type` | `contract`, `system` or `diagnostic` |
| `tx_hash` | Producing transaction hash |
| `tx_index` | Transaction index within the ledger |
| `op_index` | Operation index within the transaction |
| `in_successful_call` | `true` or `false` |
| `topics` | Decoded topics as a JSON array inside one cell |
| `value` | Decoded value as JSON inside one cell |
| `ledger_closed_at` | On-chain close time, RFC3339 (UTC) |
| `created_at` | Ingest time, RFC3339 (UTC) |

`topics` and `value` are JSON documents, not scalars, so they are written as JSON text inside the cell (e.g. `"[{""symbol"":""transfer""}]"`) and must be parsed by the consumer. Query them with a JSON-aware tool if the spreadsheet is not enough. An absent topics or value is an empty cell. Values are **not** prefixed to neutralise spreadsheet formula injection (`=`, `+`, `-`, `@`): contract events are data and rewriting them would corrupt legitimate values. The file is served as an attachment, but consumers importing untrusted contract data should apply their own sanitisation.

**Streaming and bounds:**
- Rows are written as they are read and flushed periodically; the server does not accumulate the result in memory.
- A single export is capped at **100,000 rows**. Clients needing more should narrow the filters or page using the JSON API. `limit` is ignored (an export covers the full result); `cursor` is honoured to resume.
- The write loop checks the request context, so a client that disconnects stops the work.
- Once the first byte is sent the status code is committed. A backend failure or a client disconnect therefore ends the response with a **truncated file** — complete rows only, with no final marker. Treat a short CSV/NDJSON file as incomplete rather than corrupt.
- **Upstream mode:** exports work against SoroTrail too, but that backend answers by scanning ledger windows rather than reading an index, so a large export costs many upstream requests. Prefer a ledger range or a contract scope for anything big.

## Pagination

List responses include `next_cursor`; pass it back as `cursor` for the next page. A null `next_cursor` means you've reached the end. Treat the cursor as opaque.

## Rate limiting

Rate limiting is **off by default**. Set both `RATE_LIMIT_RPS` (sustained requests per second) and `RATE_LIMIT_BURST` (instantaneous allowance) to enable a per-client token bucket over `/api`. A partial pair is rejected at startup rather than guessed at.

When a client exceeds its allowance the response is `429 Too Many Requests` with a `Retry-After` header in whole seconds and the usual JSON error envelope:

```json
{
  "error": "rate limit exceeded; retry after 2 seconds",
  "code": "Too Many Requests",
  "request_id": "..."
}
```

`Retry-After` comes from the limiter's actual token state, so it is the real wait until capacity returns rather than a fixed constant.

**Key:** the socket's remote address. Behind a proxy, set `RATE_LIMIT_TRUSTED_PROXY=true` to key on the first `X-Forwarded-For` entry instead — only safe when the proxy overwrites that header, since a client can otherwise choose its own bucket.

**Exempt:** `/health`, `/livez`, `/readyz`, `/metrics`, and the web UI (its htmx partials are ordinary page loads and a limit tuned for API clients must not lock out a browsing session). Only `/api` is limited.

Rejections increment `sorovista_http_rate_limited_total`, so an operator can tell a tight limit from real abuse.

## Errors

Errors return an envelope with an HTTP status matching the failure class — `400` for invalid input (naming the offending parameter), `404` for an unknown contract or event, `500` for server faults. In upstream mode, an unreachable SoroTrail instance surfaces as a `502`-class error rather than a silent empty result.
