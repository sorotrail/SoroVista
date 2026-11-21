# Browsing contracts

The contracts list is SoroVista's home page — every contract it has event data for, however it's getting that data.

## The contracts list

Visit `http://localhost:8080` (or `/contracts`) to see every known contract, each showing:

- Contract ID
- Total event count
- First and last seen ledger
- Time of most recent activity

The list is paginated and searchable by contract ID — paste a full or partial `C...` address into the search box to jump to it.

The same list is available as JSON:

```bash
curl 'http://localhost:8080/api/contracts?limit=1'
```

```json
{
  "contracts": [
    {
      "id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
      "event_count": 128,
      "first_ledger": 1234567,
      "last_ledger": 1234999,
      "last_activity": "2024-01-01T00:00:00Z"
    }
  ]
}
```

## The contract detail page

Clicking a contract opens its event history: every event it has emitted, newest-first, with:

- Decoded topics (e.g. an event name like `transfer`, plus argument values) shown readably rather than as raw ScVals
- The decoded value payload
- Ledger, transaction hash, and success status
- Basic stats: total events, and a breakdown by event type

From here you can filter by event type or ledger range — see [Searching and filtering events](searching-events.md).

## Finding a contract you don't have the ID for

If you know roughly what you're looking for but not the exact contract ID, the global search accepts partial matches on contract ID. There's no name-based search in the MVP (SoroVista doesn't resolve contract metadata like token names) — you need the `C...` address. If you're coming from a wallet or another explorer, copy the address from there.

Pass a partial ID as `search` to narrow the list:

```bash
curl 'http://localhost:8080/api/contracts?search=CDLZ&limit=10'
```

Matching is a case-insensitive substring test on the contract ID, so `CDLZ` returns every contract whose address contains those characters. In the UI, type the same fragment into the search box on `/contracts` and submit.

## What "seen" means

A contract only appears in the list once SoroVista has ingested at least one of its events — in standalone mode, that means since SoroVista started running (or since `START_LEDGER`, if you set one); in upstream mode, it means SoroTrail has an event for it. A contract with no events yet, or one SoroVista hasn't been watching, won't appear.
