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

**Example:** restart SoroVista in upstream mode:

```bash
SOURCE_MODE=sorotrail SOROTRAIL_URL=https://your-sorotrail-instance.example.com make run
```

Switching modes changes where data comes from going forward — it doesn't migrate or merge history between the two. If you switch from standalone to upstream, you're now viewing SoroTrail's history, not a combination of both.

## Choosing at a glance

| | Standalone | Upstream |
|---|---|---|
| Extra service required | No (just Postgres) | Yes — a SoroTrail instance |
| History depth | Bounded by RPC retention | As deep as SoroTrail's own history |
| Simplest to start | ✅ | Needs SoroTrail already running |
| Best for | Quick exploration, standalone deployments | Serious/production use alongside SoroTrail |
| Paging cost per page | Low (single SQL query) | 2–4 upstream requests; grows with sparsity |
| Contract listings | Exact (from SQL index) | Approximate (bounded scan of recent events) |
| Stats accuracy | Exact (`approximate: false`) | `approximate: true` for type breakdown; totals exact |
| Zero `first_ledger`/`last_ledger` | Unknown = 0 (DB empty) | Unknown = 0 (limited SoroTrail history) |

**Paging advice:** In upstream mode, specify `from_ledger` / `to_ledger` when possible to
keep the scan window narrow and the request count low. Narrow filters without ledger bounds
will scan many windows.

**Stats note:** The `approximate` flag on `/api/stats` is `false` in standalone mode
(where everything is counted in SQL) and `true` in upstream mode (where the type breakdown
is from a bounded scan; totals themselves are exact from SoroTrail).

**Cursor portability:** Cursors are opaque and not portable between modes. A cursor from
standalone mode cannot be used with upstream mode and vice versa. Discard cursors when
switching modes.

**Invalid cursor:** Returns `400 Bad Request` with an error naming the `cursor` parameter.

**Example:** start SoroVista in either mode:

```bash
# Standalone (needs Postgres)
SOURCE_MODE=rpc DATABASE_URL=postgres://sorovista:sorovista@localhost:5432/sorovista?sslmode=disable make run

# Upstream (needs a running SoroTrail instance)
SOURCE_MODE=sorotrail SOROTRAIL_URL=http://localhost:8080 make run
```
