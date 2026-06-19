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
│   endedAccumulator []*MetricFamily                            │
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

`MetricsService.gatherHandlerMetrics` collects two pools of metrics on
every scrape and feeds them into `mergeFamilies`:

1. **Live handlers** — each `Process.Gather()` returns whatever its IPC peer
   currently reports, or an empty slice if the handler has been marked
   finalized (see below) or if its IPC call fails.
2. **Persistent accumulator** (`endedAccumulator`) — running totals of
   counter/histogram values from all handlers that have reported their
   final snapshot via `StoreProcessEndedMetrics`. Bounded in size by label
   cardinality, not by handler history.

`mergeFamilies` groups by family name, then within each family groups
metrics by their full label set, then aggregates:

| Metric type | Aggregation                                              |
|-------------|----------------------------------------------------------|
| Counter     | sum values                                               |
| Gauge       | sum values                                               |
| Untyped     | sum values                                               |
| Histogram   | sum `sample_count`, `sample_sum`, per-bucket `cumulative_count` matched by `upper_bound` |
| Summary     | sum `sample_count` and `sample_sum`; drop quantiles      |

`mergeFamilies` treats inputs as read-only and returns freshly-allocated
families/metrics. This is what lets us safely re-merge the persistent
accumulator on every gather without aliasing the accumulator's state.

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
        ├─ pm.MarkMetricsFinalized(egressID)  → Process.metricsFinalized = true
        └─ endedAccumulator = merge(endedAccumulator, parsed families)

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
3. **Handler subprocesses do not re-register the Go collector.** This keeps
   `go_*` / `process_*` flowing only from the service's
   `prometheus.DefaultRegisterer`, so the default gatherer does not collide
   with the merged gatherer.
4. **All handler-side counters are pure counters.** The merge sums all
   counter and gauge values; for counters and the channel-size gauges this
   is correct. A new gauge that represents an instantaneous level shared
   across handlers (rather than a per-handler quantity) would need a
   per-handler distinguishing label or a different aggregation rule.
5. **Quantiles on summaries are not consumed downstream.** `mergeFamilies`
   drops them since quantiles are not addable across handlers. The egress
   pipeline does not currently expose summaries, so this is a guard for
   future use.
6. **`endedAccumulator` cardinality is bounded.** It scales with the
   number of distinct label sets across families, not with handler
   history. The current label set is at most `len(type) × len(status)`
   for `pipeline_uploads` and similar small products for the others, so
   total memory is negligible.

## Per-egress gauges

`segments_uploads_channel_size` and `playlist_uploads_channel_size` keep
`egress_id` as a const label because they describe per-egress
instantaneous level (depth of an unbounded channel). Summing them across
egresses is meaningful (total pending across the node), but the per-egress
values are also useful for spotting one stuck egress, so we keep both
options open by leaving the cardinality on the handler side.

These gauges flow through `mergeFamilies` like everything else but, since
each handler tags them with a distinct `egress_id`, they end up in
distinct label-set buckets and pass through unsummed.

## Testing

`pkg/service/metrics_test.go` covers:

- counter aggregation across two families with identical labels;
- distinct label sets within one family passing through unmerged
  (verifies the channel-size gauge case);
- histogram aggregation: `sample_count`, `sample_sum`, per-bucket
  `cumulative_count`;
- single-family pass-through (no-op merge);
- accumulator isolation — `mergeFamilies` must not mutate inputs, so the
  same accumulator can be reused across consecutive scrapes.
