# Operating modes: standalone vs upstream

SoroVista can get its event data two ways. Pick based on what you already run and how deep you need history to go.

## Standalone mode (`SOURCE_MODE=rpc`)

SoroVista polls Stellar RPC directly and keeps its own Postgres store, the same way a lightweight indexer would.

**Use it when:**
- You want SoroVista running with nothing else — just it and a database.
- You're exploring recent activity, not reconstructing deep history.
- You don't already run a SoroTrail instance.

**Limits:** Stellar RPC only retains contract events for a short window (roughly 24 hours to 7 days). Standalone SoroVista only knows about events it has personally ingested while running — history from before it started, or from any downtime longer than the RPC's retention, is simply not there.

## Upstream mode (`SOURCE_MODE=sorotrail`)

SoroVista reads from a running [SoroTrail](https://github.com/sorotrail/SoroTrail) instance's API instead of polling the RPC itself.

**Use it when:**
- You already run SoroTrail (or have access to one) and want a browsable UI on top of its data.
- You need history deeper than the RPC's retention window — SoroTrail has been accumulating it durably.
- You'd rather not run two separate ingestion loops against the same RPC.

**Limits:** SoroVista's history is exactly SoroTrail's history — no more, no less. If SoroTrail only started indexing recently, upstream mode won't show anything older than that either.

### Paging, contract listings, and stats accuracy in upstream mode

Because upstream mode keeps no local database and SoroTrail's API only paginates events ascending by TOID (with no dedicated `GET /contracts` endpoint), SoroVista adapts its queries in three ways that callers should keep in mind:

1. **Backwards window paging:** To return events newest-first, SoroVista walks adaptive ledger windows backwards from the chain tip (or `to_ledger`), reversing each window in memory. A typical page takes two to four upstream requests, but a sparse filter (`topic`, `type`, or an inactive contract) across a wide ledger range must walk more windows (capped at 24 windows per request). Passing `from_ledger` and `to_ledger` keeps upstream queries fast and bounded.
2. **Derived contract listings:** `GET /api/contracts` (and `/contracts` in the UI) is aggregated from a bounded backwards scan of up to `5,000` recent events and cached for `30s`. Contracts whose latest activity is older than the most recent `5,000` events will not appear in the derived contract list, even though querying `/contracts/{id}` or `/api/contracts/{id}/events` directly still reaches their full history in SoroTrail.
3. **Approximate breakdowns vs exact totals:** On `GET /api/stats`, `total_events`, `contract_count`, and `last_ledger` come directly from SoroTrail and are exact, whereas `type_breakdown` and `first_ledger` are derived from the bounded recent-event scan. On `GET /api/contracts/{id}/stats` and `GET /api/events/buckets`, counts and breakdowns are likewise derived from a bounded scan (`5,000` events) and flagged with `"approximate": true` whenever the scan does not exhaust the upstream history (and `"approximate": false` when all history for that query fit within the scan). Across all stats responses, a `first_ledger` or `last_ledger` value of `0` means **unknown** (no events observed in the scanned range).

## Switching modes

Set `SOURCE_MODE` and the corresponding URL, then restart:

```bash
# standalone
SOURCE_MODE=rpc
RPC_URL=https://soroban-testnet.stellar.org
DATABASE_URL=postgres://...

# upstream
SOURCE_MODE=sorotrail
SOROTRAIL_URL=https://your-sorotrail-instance.example.com
```

Switching modes changes where data comes from going forward — it doesn't migrate or merge history between the two. If you switch from standalone to upstream, you're now viewing SoroTrail's history, not a combination of both. Pagination cursors are also specific to the backend that minted them: any cursor saved by a client before a mode switch is invalid on the new mode and must be discarded.

## Choosing at a glance

| | Standalone (`SOURCE_MODE=rpc`) | Upstream (`SOURCE_MODE=sorotrail`) |
|---|---|---|
| Extra service required | No (just Postgres) | Yes — a SoroTrail instance |
| History depth | Bounded by RPC retention (while running) | As deep as SoroTrail's own history |
| Event paging cost | Single indexed Postgres query per page | Adaptive backwards ledger-window scan (2–4 upstream calls typical; more for sparse filters unless bounded by `from_ledger`/`to_ledger`) |
| Contract listings (`/api/contracts`) | Exact (indexed SQL over all stored events) | Approximate — derived from up to `5,000` recent events (cached 30s) |
| Stats & type breakdowns | Exact (`"approximate": false`) | Exact global totals from SoroTrail; scan-derived `type_breakdown` and per-contract stats flagged `"approximate": true` when history exceeds scan budget |
| Cursors portable across modes | No (opaque, backend-specific) | No (opaque, backend-specific) |
| Live SSE stream (`/api/events/stream`) | Supported | Returns `501 Not Implemented` (poll `/api/events` with `cursor` instead) |
| Simplest to start | ✅ | Needs SoroTrail already running |
| Best for | Quick exploration, self-contained deployments | Deep history alongside an existing SoroTrail indexer |
