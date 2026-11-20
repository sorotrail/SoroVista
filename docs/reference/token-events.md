# SEP-41 token annotation

Event responses carry an optional `token` object when an event matches the
layout SEP-41 token contracts use. It resolves the event into named slots, so a
client can say that one address sent an amount to another without hard-coding
topic positions.

## Shape, not claim

The annotation describes the **shape** of the event, not a verified interface
claim.

Matching is structural: the first topic names a known event, the two address
topics are addresses, and the value carries an `i128` where the event has an
amount. A contract that emits a transfer-shaped event without implementing
SEP-41 is indistinguishable from one that does, and the annotation is honest
either way. Treating `token` as proof that a contract implements SEP-41 would
be wrong; it is a reading of one event.

## Recognised events and topic layout

All five events put the event name in topic 0 and two addresses in topics 1
and 2. What those addresses mean is **not uniform** — getting it wrong in a
client inverts the direction of a transfer.

| Event | Topic layout | `from` | `to` | Amount |
|---|---|---|---|---|
| `transfer` | `{sym:"transfer"}, addr:from, addr:to` | sender | recipient | `i128` |
| `mint` | `{sym:"mint"}, addr:admin, addr:to` | admin | recipient | `i128` |
| `burn` | `{sym:"burn"}, addr:admin, addr:from` | holder | admin | `i128` |
| `clawback` | `{sym:"clawback"}, addr:admin, addr:from` | holder | admin | `i128` |
| `set_admin` | `{sym:"set_admin"}, addr:old, addr:new` | old admin | new admin | none |

In other words:

- **transfer**: `from` is the sender and `to` is the recipient.
- **mint**: `from` is the admin, `to` is the recipient. No address "sends" in
  the ordinary sense; the admin is the issuing side.
- **burn** and **clawback**: topic 1 carries the admin and topic 2 the holder,
  but the annotation reports `from` as the holder and `to` as the admin, since
  the holder is the side the amount leaves.
- **set_admin**: `from` and `to` are the old and new admins. There is no amount.

## JSON shape

```json
"token": {
  "event": "transfer",
  "from": "G…",
  "to": "G…",
  "amount": "26039"
}
```

- `event` is always present.
- `from` and `to` are omitted when empty.
- `amount` is omitted for `set_admin`, which carries no amount.
- `amount` is always a string, never a JSON number, so clients can parse it as
  a big integer.

Fields are omitted rather than null, so a client can test for their presence.

## When the annotation is absent

`token` does not appear on an event when any of the following is true:

- `topics` is not a JSON array.
- There are fewer than three topics.
- The first topic is not a recognised event name. Both decoded shapes are
  accepted for a name — a bare string (`"transfer"`) or the single-key wrapper
  (`{"symbol":"transfer"}`) — and anything else is not a name.
- Either address slot is not an address. Both decoded shapes are accepted here
  too: a bare string (`"G…"`) or the wrapper (`{"address":"G…"}`).
- The event is one of the amount-bearing events and its value has no `i128`.
  Such an event still gets `event`, `from` and `to`, but no `amount`.

## Known limitation with large amounts

Amounts stored as `i128` strings pass through exactly. A known open bug affects
values that reach the annotation as JSON numbers: the stored value has already
been through `float64`, so a large amount can lose precision, and the
annotation renders it back from that lossy number. Tracked in the
[open precision issue](https://github.com/sorotrail/SoroVista/issues?q=is%3Aissue+precision);
until it is fixed, treat `amount` on such events as approximate rather than
exact.

## Worked example

A real `GET /api/events/{id}` response from a local run:

```json
{
  "id": "0016880389704384512-0000000003",
  "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
  "ledger": 3930272,
  "type": "contract",
  "tx_hash": "bcd19d1ae7ca70115c19a5098652b242e5c98c9f2ed9355f07433f99d84920dd",
  "tx_index": 0,
  "op_index": 0,
  "in_successful_call": true,
  "topics": [
    {
      "symbol": "transfer"
    },
    {
      "address": "GDS2XSFBG5KQ3G3UNGSA6EX6E4OS3CSBS3NHFS7AGWZP67KD7T46HQJH"
    },
    {
      "address": "GBX7F6J6KQ3G3UNGSA6EX6E4OS3CSBS3NHFS7AGWZP67KD7T46HQJHAB"
    }
  ],
  "value": {
    "i128": "26039"
  },
  "ledger_closed_at": "2026-08-02T12:43:57+01:00",
  "created_at": "2026-08-03T12:28:52.587273+01:00",
  "token": {
    "event": "transfer",
    "from": "GDS2XSFBG5KQ3G3UNGSA6EX6E4OS3CSBS3NHFS7AGWZP67KD7T46HQJH",
    "to": "GBX7F6J6KQ3G3UNGSA6EX6E4OS3CSBS3NHFS7AGWZP67KD7T46HQJHAB",
    "amount": "26039"
  }
}
```

The annotation appears on every endpoint that returns events — `GET
/api/events`, `GET /api/contracts/{id}/events`, `GET /api/events/{id}` and the
SSE stream. The surrounding event fields are covered in the
[data model](data-model.md) reference.
