# Data model

What SoroVista stores depends on its mode.

## Standalone mode

A small schema, mirroring what a lightweight indexer needs:

| Table | What it holds |
|---|---|
| `events` | Stored events: id (TOID, PK), contract id, ledger, type, tx hash, tx/op indices, decoded topics (jsonb), decoded value (jsonb), success flag, timestamp |
| `ingest_state` | The poller's progress: last processed ledger and pagination cursor |

Events are upserted on their id, so re-reading an overlapping ledger range never creates duplicates.

### Zero-padded TOID ordering and cursor pagination

Every event's `id` is a zero-padded Total Order ID (TOID) string (for example, `0016880389704384512-0000000003`) encoding the ledger sequence, transaction index, operation index, and event index with fixed-width zero padding.

This zero-padded format is load-bearing across the entire codebase: **lexicographic string order on `id` is identical to chronological chain order** (ledger → transaction → operation → event index). Both backends rely on this invariant:

- In **standalone mode**, descending event pagination (`ORDER BY id DESC` with `id < $cursor`) uses the primary-key index directly without parsing the TOID components.
- In **upstream mode**, the backwards ledger-window scanner (`internal/source/sorotrailsource/scan.go`) compares event IDs lexicographically (`e.ID < beforeID` and descending sort by `ID`) to deduplicate overlapping windows and resume pagination strictly below the last event of the previous page.

A standalone instance is configured with `SOURCE_MODE=rpc` and a Postgres `DATABASE_URL`; the poller owns the two tables above. Point `psql` at that database to see exactly what it has accumulated:

```bash
psql "$DATABASE_URL" -c 'SELECT last_ledger FROM ingest_state WHERE id = 1;'
psql "$DATABASE_URL" -c 'SELECT id, contract_id, ledger, type, ledger_closed_at FROM events ORDER BY id DESC LIMIT 3;'
```

The first query returns the poller's resume point; after a restart it continues from here instead of re-reading from the retention horizon. The second query reads newest-first, the same ordering the API and UI use:

```text
 last_ledger
-------------
     3930272

              id               |              contract_id               | ledger  |   type   |    ledger_closed_at
-------------------------------+----------------------------------------+---------+----------+------------------------
 0016880389704384512-0000000003 | CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF4… | 3930272 | contract | 2026-08-02 11:43:57+00
 0016880389704384512-0000000002 | CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF4… | 3930272 | contract | 2026-08-02 11:43:57+00
 0016880389704384512-0000000001 | CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF4… | 3930271 | contract | 2026-08-02 11:43:52+00
```

## Upstream mode

SoroVista holds **no local event data** in upstream mode. Every contract and event query is served live from the connected SoroTrail instance's API. There is no local database requirement, no migrations to run, and no local copy to keep in sync — SoroVista is a thin, stateless window onto SoroTrail's own store.

This has a practical consequence: SoroVista's availability and freshness in upstream mode are bounded by SoroTrail's. If the SoroTrail instance is down or behind, SoroVista reflects that immediately, since it isn't caching anything of its own (aside from a 30-second in-memory cache of the recent-event contract list).

## Choosing based on this

If you want SoroVista to have its own independent copy of data (for example, to survive a SoroTrail outage, to get exact SQL-backed contract listings and breakdowns, or to run fully self-contained in your own environment), standalone mode is the only option that gives you local storage. If you're fine depending on a live SoroTrail instance and want zero extra database to run, upstream mode is simpler.

## Backups

Standalone mode: back up the Postgres database (`pg_dump`) the same as any indexer — it holds accumulated history the RPC can no longer reproduce. Upstream mode: nothing to back up on the SoroVista side; back up the SoroTrail instance's database instead, since that's where the actual data of record lives.
