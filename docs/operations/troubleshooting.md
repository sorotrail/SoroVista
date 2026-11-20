# Troubleshooting

SoroVista has several failure modes that look identical from the outside: the
pages load, `/health` answers, and the explorer is empty. They have completely
different causes. This page starts with the most common one and works through
the others, each with the symptom you see first, then why it happens, then what
to check.

## The explorer is empty on a fresh install

**Symptom.** The UI loads and reports a healthy source, but there are no
contracts and no events. `/api/stats` shows `"total_events": 0` and
`"approximate": false`.

**Cause.** This is usually not a fault. In standalone mode (`SOURCE_MODE=rpc`)
SoroVista polls Stellar RPC itself and stores only what it captures while it is
running. A new instance therefore starts with nothing to show. The UI says so
in a banner on every page, and `/health` carries the same caveat in
`retention_note`:

> Standalone mode captures events only while SoroVista is running: Stellar RPC
> retains contract events for about 24 hours to 7 days, so history from before
> this instance started is not available. Point SOURCE_MODE at a SoroTrail
> indexer to browse deeper history.

**What to check.**

- Look for the "Mode: rpc" banner, or `curl -s localhost:8080/health` and read
  `retention_note`. If it is there, the instance is working as designed.
- Wait at least one poll cycle (`POLL_INTERVAL`, 5s by default) plus a few
  ledgers of chain activity. A caught-up poll with nothing new logs
  `no new events` at debug level; a productive one logs
  `ingested events count=… from_ledger=… to_ledger=…`. An empty poll is not a
  failure: the ingester stops following the RPC cursor as soon as a page comes
  back short, so a drained window is the ordinary way a poll ends.
- Confirm the chain is producing contract events in the window: the public
  testnet is busy, a private network may genuinely be quiet.
- If events never arrive at all, work through the sections below — in
  particular `WATCHED_CONTRACTS` and the network check.

There is no way to make a standalone instance see history that predates it.
That is a property of the RPC's retention, not a setting.

## History from before the instance started is missing

**Symptom.** The explorer shows recent activity only. `/api/stats` reports a
`first_ledger` roughly a day before `last_ledger`, even though the chain is much
older.

**Cause.** With no `ingest_state` row, a cold start begins at the chain tip
minus `RETENTION_LEDGERS` (default `17280`, about 24 hours at 5s per ledger).
Stellar RPC itself keeps contract events for only roughly 24 hours to 7 days,
so even a larger reach-back cannot produce history the node has already
discarded. Reaching past the node's own window simply yields nothing, which
looks like a broken ingester.

**What to check.**

- `/api/stats`: `first_ledger` is the earliest event actually captured.
- The cold-start log line: `ingested events … from_ledger=…` shows where the
  first poll began.
- `RETENTION_LEDGERS` and `START_LEDGER`. `START_LEDGER` forces a specific cold
  start; setting it does not create data the RPC can no longer serve.
- In Postgres, `SELECT last_ledger FROM ingest_state;` shows the resume point
  a restart will continue from.

For deeper history, run a SoroTrail indexer and point SoroVista at it with
`SOURCE_MODE=sorotrail`. There is no standalone-mode remedy for events the RPC
has already aged out.

## Nothing is ingested, but the service reports healthy

**Symptom.** `/health` is healthy, `latest_ledger` advances, yet the explorer
stays empty or shows only a fraction of the network. The logs repeat
`no new events`.

**Cause.** `WATCHED_CONTRACTS` silently filters everything else out. A
well-formed but wrong contract ID passes startup validation and then matches
nothing, so the instance ingests zero events while looking entirely healthy.
Only `contract` events are ingested at all; `system` and `diagnostic` events
never appear.

**What to check.**

- The ingester's start line reports the filter:
  `ingester starting watched_contracts=N …`. `N` should match the number of IDs
  you intended to watch.
- `/api/contracts` lists only contracts with captured events. An empty list
  with a non-zero `watched_contracts` means the filter is excluding everything.
- Unset `WATCHED_CONTRACTS` temporarily and restart. If events appear, the
  watched list is the problem.
- A genuinely malformed ID fails startup rather than filtering silently, with
  `invalid contract ID "…" in WATCHED_CONTRACTS`.

## The service starts against the wrong network

**Symptom.** One of two things:

- Startup fails with
  `network mismatch: configured for testnet ("Test SDF Network ; September 2015") but the RPC endpoint belongs to mainnet ("Public Global Stellar Network ; September 2015") — check NETWORK / RPC_URL`.
- Or startup continues after a warning:
  `could not verify network passphrase`. The explorer may then show nothing
  because it is pointed at a different chain than was intended.

**Cause.** A network's passphrase is its identity. SoroVista asks the RPC which
network it belongs to at startup. A successful check with a different
passphrase is fatal and stops the process. If the RPC cannot answer that call
at all — unreachable, or an older node without `getNetwork` — SoroVista logs
the warning and starts anyway, leaving the deployment unverified.

**What to check.**

- `NETWORK`, `RPC_URL` and `NETWORK_PASSPHRASE`. A named network preset supplies
  both the passphrase and the endpoint; `RPC_URL` overrides only the endpoint,
  which is how a mainnet endpoint ends up behind testnet configuration.
- `/health`: the `detail` names an unreachable RPC or database.
- Treat the warning as unresolved until the endpoint answers. The
  `network verified` line is logged on the same startup path whether or not the
  verification call succeeded, so the absence of the warning is the signal that
  a check actually ran.

The remedy is to correct `NETWORK`/`RPC_URL` — or, for a private network, set
`NETWORK=custom` with matching `RPC_URL` and `NETWORK_PASSPHRASE`. If the node
cannot answer `getNetwork`, nothing in SoroVista can verify the chain for you;
point it at an endpoint that can.

## Logs warn that xdrFormat=json was rejected

**Symptom.** A warning appears once, early in the logs:

> stellar rpc rejected xdrFormat=json; decoding base64 XDR locally instead

**Cause.** SoroVista prefers to receive contract events already decoded as
JSON, but older RPC nodes only return base64 XDR. On the first rejection it
switches to decoding XDR locally and latches that choice, so only the first
request pays for the detection.

**What to check.**

- Nothing, usually. The fallback is correct behaviour and events are stored in
  exactly the same decoded shape either way.
- If events are missing or decoding fails entirely, confirm the node is a
  Stellar RPC endpoint (JSON-RPC 2.0) and that it serves `getEvents`; the
  warning itself is not an error to fix.
- `/api/events/{id}` should still show decoded topics and values. If it shows
  nothing, the problem is upstream of the fallback — check the poll logs.

## `/health` returns 503

**Symptom.** `GET /health` answers `503` with `"healthy": false`, and every page
carries a "Source unavailable" banner.

**Cause.** The configured source cannot reach a dependency. The `detail` field
names which one:

| `detail` begins with | Meaning |
|---|---|
| `database unreachable:` | Postgres in standalone mode is down or `DATABASE_URL` is wrong. |
| `stellar rpc unreachable:` | The RPC endpoint cannot be reached in standalone mode. |
| `stellar rpc reports "…"` | The RPC answered but does not report itself healthy. |
| `sorotrail unreachable:` | The upstream indexer in upstream mode cannot be reached. |
| `sorotrail reports …` | The upstream indexer answered with a failing health check. |

**What to check.**

- Read `detail` from `/health` and `curl -s localhost:8080/readyz`, which
  reports the same view.
- For `database unreachable`, check `DATABASE_URL` and that Postgres accepts
  connections; migrations run at startup, so a database that came up later than
  SoroVista still needs a restart.
- For `stellar rpc unreachable`, check `RPC_URL` and network egress.
- For `sorotrail unreachable`, check `SOROTRAIL_URL` and the indexer's own
  health endpoint.
- `/livez` answers `200` regardless: it reports that the process is alive, not
  that its backend is. A restarting container that is 503 on readiness but 200
  on liveness is not stuck in a restart loop by SoroVista's design.
