# Metrics

SoroVista serves Prometheus instrumentation at `/metrics` in both operating
modes. The endpoint is deliberately unauthenticated, like the rest of the
read surface: it exposes route timings and ingest counters, never event data.
A `nil` `*Metrics` records nothing, so instrumentation can never be
load-bearing — a deployment without metrics behaves exactly the same.

## Metric reference

Every metric is registered by `metrics.New()` in `internal/metrics/metrics.go`.
Histograms appear on the wire as three series (`_bucket`, `_sum` and `_count`).

| Metric | Type | Labels | Modes | Meaning |
|---|---|---|---|---|
| `sorovista_polls_total` | counter | `outcome` (`ok` or `error`) | standalone | Completed ingest poll cycles, split by whether the cycle succeeded. |
| `sorovista_poll_duration_seconds` | histogram | none | standalone | Wall-clock duration of a single poll cycle. A cycle that slows down without failing shows up here before it shows up as lag. |
| `sorovista_ingest_lag_ledgers` | gauge | none | standalone | How far the stored resume point trails the chain tip, in ledgers. It is the direct measure of how much history an instance is missing. |
| `sorovista_events_ingested_total` | counter | none | standalone | Contract events received from the RPC and written to the store. Re-reading an overlapping ledger range does not double-count what was already stored. |
| `sorovista_seconds_since_last_poll` | gauge | none | standalone | Seconds since the ingester last completed a cycle. It grows without bound when polling has stopped, which makes it the simplest stall signal. |
| `sorovista_http_request_duration_seconds` | histogram | `route`, `method`, `status` | both | HTTP request duration, bucketed with Prometheus's default buckets. |

The four ingest metrics are meaningful only in standalone mode. Upstream mode
runs no ingester, so they stay at zero; treat that as "not applicable" rather
than as an outage. The HTTP duration metric is populated in both modes.

## Labels

`outcome` on `sorovista_polls_total` is a closed set: `ok` or `error`.

`sorovista_http_request_duration_seconds` is labelled by the **matched route
pattern** (for example `/api/events/{id}`), not the raw request path. That is
what keeps label cardinality bounded — a route with a user-supplied ID would
otherwise create a new time series per ID, so the metric is safe to aggregate
across all requests.

## Useful queries

An ingest stall is the failure an operator most needs to hear about: the
process is up and serving, but no longer keeping up with the chain. The
seconds-since-last-poll gauge climbs whenever the ingester stops completing
cycles:

```promql
sorovista_seconds_since_last_poll > 120
```

As an alerting rule:

```yaml
- alert: SoroVistaIngestStalled
  expr: sorovista_seconds_since_last_poll > 120
  for: 5m
  annotations:
    summary: SoroVista has not completed a poll cycle in over two minutes
```

A falling-behind instance is different from a stalled one: it is still
polling, but the chain tip is moving away from the stored resume point.

```promql
sorovista_ingest_lag_ledgers > 100
```

The error rate over recent poll cycles, as a fraction of all cycles:

```promql
sum(rate(sorovista_polls_total{outcome="error"}[5m]))
  /
sum(rate(sorovista_polls_total[5m]))
```

Request latency by route, using the histogram's buckets:

```promql
histogram_quantile(0.95,
  sum by (route) (rate(sorovista_http_request_duration_seconds_bucket[5m])))
```
