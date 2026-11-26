# Quickstart

From zero to browsing contract events in a few minutes. You need Docker with the compose plugin.

## 1. Clone and start the stack

```bash
git clone https://github.com/sorotrail/SoroVista.git
cd SoroVista
cp .env.example .env
docker compose up -d
```

By default this runs in **standalone mode**, polling the public Stellar testnet RPC and storing events in its own Postgres. Confirm it's healthy:

```bash
curl http://localhost:8080/health
```

Sample response payload:

```json
{
  "mode": "rpc",
  "healthy": true,
  "latest_ledger": 512000
}
```

## 2. Open the UI

Visit `http://localhost:8080` in a browser. You'll land on the contracts list — initially empty, filling in as SoroVista ingests testnet activity (give it a minute on an active network).

**Example:** Open the UI in a browser or verify the endpoint is serving:

```bash
curl -I http://localhost:8080
```

## 3. Browse a contract

Search for a contract by its `C...` ID, or click into any that appear in the list. The contract page shows its events newest-first, with decoded topics and values, filterable by event type and ledger range.

**Example:** Navigate to a contract in the browser:

```bash
http://localhost:8080/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC
```

Or query its summary via the API:

```bash
curl 'http://localhost:8080/api/contracts/CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC'
```

## 4. Inspect an event

Click any event row to see it fully expanded: decoded topics, decoded value, ledger, transaction hash, and whether the emitting call succeeded.

**Example:** View an event detail page in the browser:

```bash
http://localhost:8080/events/0016880389704384512-0000000003
```

Or fetch the full event JSON via the API:

```bash
curl 'http://localhost:8080/api/events/0016880389704384512-0000000003'
```

Sample response payload:

```json
{
  "id": "0016880389704384512-0000000003",
  "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
  "ledger": 512000,
  "type": "contract",
  "tx_hash": "a1b2c3d4e5f67890abcdef1234567890abcdef1234567890abcdef1234567890",
  "in_successful_call": true,
  "topics": [
    {"symbol": "transfer"}
  ],
  "value": {"i128": "1000"}
}
```

## 5. Try the JSON API

Everything in the UI is also available as JSON:

```bash
curl 'http://localhost:8080/api/contracts?limit=10'
curl 'http://localhost:8080/api/contracts/CC.../events?limit=20'
curl 'http://localhost:8080/api/events/<event-id>'
```

## Want deeper history instead?

Standalone mode only sees what it's ingested since it started, because Stellar RPC retains events briefly. If you run a [SoroTrail](https://github.com/sorotrail/SoroTrail) instance, point SoroVista at it instead for a much deeper archive — see [Operating modes](../concepts/operating-modes.md).

Set these in `.env`, then restart the stack:

```bash
# .env
SOURCE_MODE=sorotrail
SOROTRAIL_URL=https://your-sorotrail-instance.example.com
```

```bash
docker compose up -d
curl http://localhost:8080/health
```

The health response reports the active source, so you can confirm it switched to `sorotrail`.

## Next steps

Try the API from the command line to see what's available before diving into the guides:

```bash
# Overview counters, then the five newest events as JSON.
curl 'http://localhost:8080/api/stats'
curl 'http://localhost:8080/api/events?limit=5'
```

- [Operating modes](../concepts/operating-modes.md) — standalone vs upstream
- [Searching and filtering events](../guides/searching-events.md)
- [REST API reference](../reference/api.md)
