# Handler Metrics Aggregation

This document describes how Prometheus metrics emitted by per-egress handler
subprocesses are exposed by the service process, and the assumptions the
design rests on.

## Goal

Each running egress lives in its own handler subprocess that exposes Prometheus
metrics. Previously every counter/histogram emitted by a handler carried an
`egress_id` label, which made the time-series cardinality grow linearly with
the number of egresses the node had ever run. The goal is to drop that
cardinality on counters and histograms — one series per `(type, status)`
across the whole node — while still letting a few intrinsically per-egress
gauges keep their `egress_id` label.

## Components

```
┌───────────── handler subprocess (one per egress) ─────────────┐
│ HandlerMonitor                                                │
│   * pipeline_uploads{type,status}                  ← counter  │
│   * pipeline_upload_response_time_ms{type,status}  ← histogr. │
│   * backup_storage_writes{output_type}             ← counter  │
│   * segments_uploads_channel_size{egress_id,…}     ← gauge    │
│   * playlist_uploads_channel_size{egress_id,…}     ← gauge    │
└───────────────────────┬──────────────────────────────┬────────┘
            scrape via  │            ipc HandlerFinished
            ipc GetMetrics                             │ (final snapshot)
                        ▼                              ▼
┌───────────────────── service process ────────────────────────┐
│ MetricsService                                                │
│   endedAccumulator []*MetricFamily   (persistent totals)      │
│   pendingMetrics   []*MetricFamily   (one-shot, drained on    │
│                                       next scrape)            │
│                                                               │
│ ProcessManager                                                │
│   activeHandlers   map[egressID]*Process                      │
└───────────────────────────────────────────────────────────────┘
```

The handler-side `HandlerMonitor` only sets `egress_id` as a const label on
the two channel-size gauges. Counters and histograms have no `egress_id`, so
two handler subprocesses emit the same series name with identical labels —
they cannot be combined by `prometheus.Gatherers.Gather()` (it rejects
duplicates), which is why the service performs an explicit merge.

## Service-side gather

`MetricsService.gatherHandlerMetrics` collects three pools of metrics on
every scrape and feeds them into `mergeFamilies`:

1. **Live handlers** — each `Process.Gather()` returns whatever its IPC peer
   currently reports, or an empty slice if the handler has been marked
   finalized (see below) or if its IPC call fails. Per-egress gauges
   (`segments_uploads_channel_size`, `playlist_uploads_channel_size`)
   appear here while the handler is alive.
2. **Persistent accumulator** (`endedAccumulator`) — running totals of
   counter/histogram values from all handlers that have reported their
   final snapshot via `StoreProcessEndedMetrics`. Bounded in size by label
   cardinality of *accumulator-eligible* families (no `egress_id`), which is
   why we partition at the fold boundary — see "Accumulator partition" below.
3. **One-shot pending buffer** (`pendingMetrics`) — the final snapshot of
   any metric that is *not* accumulator-eligible (gauges, anything with
   `egress_id`). Appended on `StoreProcessEndedMetrics`, drained and
   nil'd by `gatherHandlerMetrics` under the same mutex, so each entry is
   exposed on the first scrape after the handler exits and then gone.

`mergeFamilies` groups by family name, then within each family groups
metrics by their full label set, then aggregates:

| Metric type | Aggregation                                              |
|-------------|----------------------------------------------------------|
| Counter     | sum values                                               |
| Gauge       | **not addable** — colliding label sets return `ErrCannotMergeGauges` (first-write-wins so the output is still usable). Live-pool gauges use distinct `egress_id`s so they pass through unmerged in practice. |
| Untyped     | sum values                                               |
| Histogram   | sum `sample_count`, `sample_sum`, per-bucket `cumulative_count` matched by `upper_bound` |
| Summary     | sum `sample_count` and `sample_sum`; drop quantiles      |

`mergeFamilies` treats inputs as read-only and returns freshly-allocated
families/metrics. This is what lets us safely re-merge the persistent
accumulator on every gather without aliasing the accumulator's state.

## Accumulator partition

Before folding a finished handler's parsed families, `StoreProcessEndedMetrics`
runs them through `splitForAccumulator`, which routes each metric to one of
two destinations:

- **Pending (one-shot)** — exposed on the next scrape via `pendingMetrics`:
    - any family of type `GAUGE` (instantaneous level — has no meaning once
      its handler is gone, and `mergeFamilies` refuses to merge colliding
      gauges; routing them to pending sidesteps any future collision
      entirely);
    - any individual metric whose label set contains `egress_id` (unique
      per handler — would grow `endedAccumulator` linearly with handler
      history without ever merging).
- **Accumulator** — everything else (non-gauge metrics with no `egress_id`
  label) is folded into `endedAccumulator` as before.

A family with a mix of `egress_id` and non-`egress_id` metrics is split: the
non-`egress_id` metrics go to the accumulator, the `egress_id` ones go to
pending. The two routing rules above are independent so the partition
catches both gauges and any future per-egress counter/histogram.

The pending buffer keeps the visibility property of the previous design —
when a handler exits, its final per-egress values (e.g. depth of the
upload queue at shutdown) show up on the next `/metrics` scrape and then
disappear, matching the gauge semantics that no queue exists for a dead
egress.

The default Prometheus registry (the service's own `go_*`, `process_*`, and
`promhttp_*` metrics) is folded in via `prometheus.Gatherers{...}.Gather()`
as a separate gatherer; it does not pass through the merge step. This is
safe only because handler subprocesses are *not* re-registering the Go
collector — if they were, `go_*` would arrive on two paths and the default
gatherer would need to fold through `mergeFamilies` too.

## Lifecycle of an ending handler

The ordering of events when a handler ends gracefully is:

```
1. handler subprocess sends HandlerFinished IPC (carrying its final metric
   text) to the service
   └─ service.StoreProcessEndedMetrics(egressID, text)
        ├─ parses the text
        ├─ splitForAccumulator: partition into accumulator-eligible vs pending
        ├─ pm.MarkMetricsFinalized(egressID)  → Process.metricsFinalized = true
        ├─ endedAccumulator = merge(endedAccumulator, accumulator-eligible)
        └─ pendingMetrics  = append(pendingMetrics, pending)

2. handler subprocess exits

3. service's cmd.Wait goroutine returns, calls processEnded
   └─ pm.ProcessFinished(egressID)  [under pm.mu]
        └─ delete activeHandlers[egressID]
```

Why this avoids double-counting and counter resets:

- The `MarkMetricsFinalized` call in step 1 runs **before** the fold into
  `endedAccumulator`. From that moment on `Process.Gather()` returns empty,
  so any concurrent scrape sees the handler's contribution exactly once:
  either still as live values (if the scrape ran before the flag flipped)
  or as accumulator values (if it ran after the merge). The two never
  overlap.
- Between steps 2 and 3 the handler is still in `activeHandlers` but its
  subprocess is gone; `Process.Gather()` returns empty because of the flag
  anyway, and the accumulator already carries the final tally.
- Step 3 only removes the handler from `activeHandlers`. No metrics
  bookkeeping is needed there.

The merged emitted value is therefore non-decreasing across consecutive
scrapes (sum of monotonic series), so `rate()` is meaningful.

`HandlerFinished` is invoked exactly once per handler in
`pkg/handler/handler.go` after `controller.Run` returns. The accumulator
fold is additive, so duplicate calls for the same egress would double-count
— there is no idempotency layer in front of it.

## Restart behaviour

`endedAccumulator` is in-memory. A service restart loses it, and live
handlers are children of the service so they restart from zero too.
Prometheus sees one counter-reset event per service restart, which is the
same behaviour as before this change and is handled correctly by `rate()`.

## Assumptions

1. **Identical metric registration across handler subprocesses.** Every
   handler registers the same counter / histogram names with the same
   bucket boundaries. Histogram merge matches buckets by `upper_bound`; a
   bucket present in one handler but absent in another would be carried
   through unmerged and could produce skewed quantile estimates downstream.
2. **`HandlerFinished` is delivered before the subprocess exits.** If a
   handler crashes hard without sending the IPC, its final counter values
   are lost. We rely on the existing handler shutdown path, which sends
   `HandlerFinished` before returning from `main`. Killed-on-error
   handlers may lose a small tail of counter increments — acceptable, the
   alternative requires a sidecar pulling metrics out of `/proc`.
3. **Handler subprocesses deregister the Go and process collectors.**
   `client_golang` auto-registers `NewGoCollector` and
   `NewProcessCollector` on the default registry; `pkg/handler/handler.go`
   explicitly `Unregister`s both during `NewHandler`. This keeps `go_*` /
   `process_*` flowing only from the service's own
   `prometheus.DefaultRegisterer`, so `prometheus.Gatherers.Gather()`
   (which rejects duplicate `(name, label set)` pairs across its child
   gatherers) does not see the same series from both the service and one
   or more live handlers.
4. **All handler-side counters are pure counters.** The merge sums values
   within a family. For counters and histograms this is correct. Gauges
   are routed to `pendingMetrics` instead of `endedAccumulator` (see
   "Accumulator partition"), so a new gauge does not need a per-handler
   distinguishing label to stay safe across the start-end handover. On
   the *live* side, two running handlers exposing the same gauge with the
   same label set would no longer be silently summed — `mergeFamilies`
   returns `ErrCannotMergeGauges` (the error is logged by
   `gatherHandlerMetrics`, first-write-wins values are still returned).
   Any new gauge that represents a shared (rather than per-egress)
   instantaneous level still needs its own distinguishing label or a
   different aggregation rule.
5. **Quantiles on summaries are not consumed downstream.** `mergeFamilies`
   drops them since quantiles are not addable across handlers. The egress
   pipeline does not currently expose summaries, so this is a guard for
   future use.
6. **`endedAccumulator` cardinality is bounded.** Combined with the
   accumulator partition above, the only metrics that enter the
   accumulator are non-gauge metrics with no `egress_id` label, so
   cardinality scales with the static label products (e.g.
   `len(type) × len(status)` for `pipeline_uploads`) and not with handler
   history. Total memory is negligible.
7. **`pendingMetrics` is drained on every scrape.** Memory there is
   bounded by the number of handler exits between two consecutive
   scrapes. In normal operation that's a small number; the buffer is
   nil'd under the mutex on each `gatherHandlerMetrics` call.

## Per-egress gauges

`segments_uploads_channel_size` and `playlist_uploads_channel_size` keep
`egress_id` as a const label because they describe per-egress
instantaneous level (depth of an unbounded channel). Summing them across
egresses is meaningful (total pending across the node), but the per-egress
values are also useful for spotting one stuck egress, so we keep both
options open by leaving the cardinality on the handler side.

Visibility over a gauge's lifecycle:

- **Handler alive** — values flow from the live pool, one series per
  egress_id, updated every scrape.
- **Handler just exited** — `splitForAccumulator` routes the final
  snapshot to `pendingMetrics`. The next scrape after exit shows that
  final value exactly once.
- **Subsequent scrapes** — `pendingMetrics` has been drained; the gauge
  series for that egress is no longer reported. The accumulator never
  carries it, so cardinality does not grow with handler history.

## Testing

`pkg/service/metrics_test.go` covers:

- counter aggregation across two families with identical labels;
- distinct label sets within one family passing through unmerged
  (verifies the channel-size gauge case);
- histogram aggregation: `sample_count`, `sample_sum`, per-bucket
  `cumulative_count`;
- single-family pass-through (no-op merge);
- accumulator isolation — `mergeFamilies` must not mutate inputs, so the
  same accumulator can be reused across consecutive scrapes;
- accumulator partition — `splitForAccumulator` routes gauge families
  and metrics with an `egress_id` label to `pendingMetrics` instead of
  `endedAccumulator`, and splits mixed families correctly.
