# Browsing contracts

The contracts list is SoroVista's home page — every contract it has event data for, however it's getting that data.

## The contracts list

Visit `http://localhost:8080` (or `/contracts`) to see every known contract, each showing:

- Contract ID
- Total event count
- First and last seen ledger
- Time of most recent activity

The list is paginated and searchable by contract ID — paste a full or partial `C...` address into the search box to jump to it.

## The contract detail page

Clicking a contract opens its event history: every event it has emitted, newest-first, with:

- Decoded topics (e.g. an event name like `transfer`, plus argument values) shown readably rather than as raw ScVals
- The decoded value payload
- Ledger, transaction hash, and success status
- Basic stats: total events, and a breakdown by event type

From here you can filter by event type or ledger range — see [Searching and filtering events](searching-events.md).

The same summary is available from the API:

```bash
curl 'http://localhost:8080/api/contracts/CC...'
```

```json
{
  "contract_id": "CC...",
  "total_events": 42,
  "first_ledger": 500123,
  "last_ledger": 510456,
  "type_breakdown": [
    { "type": "contract", "count": 40 },
    { "type": "diagnostic", "count": 2 }
  ]
}
```

## Finding a contract you don't have the ID for

If you know roughly what you're looking for but not the exact contract ID, the global search accepts partial matches on contract ID. There's no name-based search in the MVP (SoroVista doesn't resolve contract metadata like token names) — you need the `C...` address. If you're coming from a wallet or another explorer, copy the address from there.

## What "seen" means

A contract only appears in the list once SoroVista has ingested at least one of its events — in standalone mode, that means since SoroVista started running (or since `START_LEDGER`, if you set one); in upstream mode, it means SoroTrail has an event for it. A contract with no events yet, or one SoroVista hasn't been watching, won't appear.

Check whether a contract has been seen by searching the contracts API:

```bash
curl -s 'http://localhost:8080/api/contracts?search=CC...' | jq '.contracts[] | {id, event_count}'
```

```json
{ "id": "CC...", "event_count": 42 }
```

An empty result means SoroVista has not ingested any events for that contract yet, so it is not listed.
