// Copyright 2023 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package service

import (
	"testing"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"
)

func counterFamily(name string, metrics ...*dto.Metric) *dto.MetricFamily {
	n := name
	t := dto.MetricType_COUNTER
	return &dto.MetricFamily{Name: &n, Type: &t, Metric: metrics}
}

func counterMetric(value float64, labels ...string) *dto.Metric {
	v := value
	return &dto.Metric{
		Label:   labelPairs(labels...),
		Counter: &dto.Counter{Value: &v},
	}
}

func histogramFamily(name string, metrics ...*dto.Metric) *dto.MetricFamily {
	n := name
	t := dto.MetricType_HISTOGRAM
	return &dto.MetricFamily{Name: &n, Type: &t, Metric: metrics}
}

func histogramMetric(count uint64, sum float64, buckets map[float64]uint64, labels ...string) *dto.Metric {
	sc := count
	ss := sum
	h := &dto.Histogram{SampleCount: &sc, SampleSum: &ss}
	for ub, cc := range buckets {
		upper := ub
		cumul := cc
		h.Bucket = append(h.Bucket, &dto.Bucket{CumulativeCount: &cumul, UpperBound: &upper})
	}
	return &dto.Metric{
		Label:     labelPairs(labels...),
		Histogram: h,
	}
}

func labelPairs(kv ...string) []*dto.LabelPair {
	if len(kv) == 0 {
		return nil
	}
	if len(kv)%2 != 0 {
		panic("labelPairs requires key/value pairs")
	}
	out := make([]*dto.LabelPair, 0, len(kv)/2)
	for i := 0; i < len(kv); i += 2 {
		name := kv[i]
		value := kv[i+1]
		out = append(out, &dto.LabelPair{Name: &name, Value: &value})
	}
	return out
}

func findMetric(t *testing.T, fam *dto.MetricFamily, labels ...string) *dto.Metric {
	t.Helper()
	want := labelSetKey(labelPairs(labels...))
	for _, m := range fam.Metric {
		if labelSetKey(m.Label) == want {
			return m
		}
	}
	t.Fatalf("metric with labels %v not found in family %s", labels, fam.GetName())
	return nil
}

func TestMergeFamilies_CounterSumSameLabels(t *testing.T) {
	in := []*dto.MetricFamily{
		counterFamily("uploads", counterMetric(3, "type", "file", "status", "success")),
		counterFamily("uploads", counterMetric(5, "type", "file", "status", "success")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, "uploads", out[0].GetName())
	require.Len(t, out[0].Metric, 1)
	require.Equal(t, 8.0, out[0].Metric[0].Counter.GetValue())
}

func TestMergeFamilies_DistinctLabelsPassThrough(t *testing.T) {
	in := []*dto.MetricFamily{
		counterFamily("uploads",
			counterMetric(3, "type", "file", "status", "success"),
			counterMetric(1, "type", "file", "status", "failure"),
		),
		counterFamily("uploads", counterMetric(5, "type", "file", "status", "success")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Metric, 2)
	require.Equal(t, 8.0, findMetric(t, out[0], "type", "file", "status", "success").Counter.GetValue())
	require.Equal(t, 1.0, findMetric(t, out[0], "type", "file", "status", "failure").Counter.GetValue())
}

func TestMergeFamilies_HistogramBucketMerge(t *testing.T) {
	in := []*dto.MetricFamily{
		histogramFamily("response_time", histogramMetric(2, 30, map[float64]uint64{
			10: 1, 100: 2,
		}, "type", "file")),
		histogramFamily("response_time", histogramMetric(3, 50, map[float64]uint64{
			10: 2, 100: 3,
		}, "type", "file")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Metric, 1)
	h := out[0].Metric[0].Histogram
	require.Equal(t, uint64(5), h.GetSampleCount())
	require.Equal(t, 80.0, h.GetSampleSum())
	require.Len(t, h.Bucket, 2)

	byBound := map[float64]uint64{}
	for _, b := range h.Bucket {
		byBound[b.GetUpperBound()] = b.GetCumulativeCount()
	}
	require.Equal(t, uint64(3), byBound[10])
	require.Equal(t, uint64(5), byBound[100])
}

func TestMergeFamilies_SingleFamilyPassThrough(t *testing.T) {
	in := []*dto.MetricFamily{
		counterFamily("uploads", counterMetric(7, "type", "file", "status", "success")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, 7.0, out[0].Metric[0].Counter.GetValue())
}

func TestMergeFamilies_GaugeCollisionReturnsError(t *testing.T) {
	in := []*dto.MetricFamily{
		gaugeFamily("queue_depth", gaugeMetric(2, "egress_id", "EG_aaa")),
		// Same family, same label set — would silently sum without the error.
		gaugeFamily("queue_depth", gaugeMetric(5, "egress_id", "EG_aaa")),
	}

	out, err := mergeFamilies(in)
	require.ErrorIs(t, err, ErrCannotMergeGauges)
	// First-write-wins: the output still carries the first occurrence so the
	// merge result is usable, not nil.
	require.Len(t, out, 1)
	require.Len(t, out[0].Metric, 1)
	require.Equal(t, 2.0, out[0].Metric[0].Gauge.GetValue())
}

func TestMergeFamilies_DistinctGaugesNoError(t *testing.T) {
	// Two gauges in the same family with distinct label sets — the normal
	// live-pool case (one per egress_id). Must not error.
	in := []*dto.MetricFamily{
		gaugeFamily("queue_depth",
			gaugeMetric(2, "egress_id", "EG_aaa"),
			gaugeMetric(3, "egress_id", "EG_bbb"),
		),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Metric, 2)
}

func gaugeFamily(name string, metrics ...*dto.Metric) *dto.MetricFamily {
	n := name
	t := dto.MetricType_GAUGE
	return &dto.MetricFamily{Name: &n, Type: &t, Metric: metrics}
}

func gaugeMetric(value float64, labels ...string) *dto.Metric {
	v := value
	return &dto.Metric{
		Label: labelPairs(labels...),
		Gauge: &dto.Gauge{Value: &v},
	}
}

func TestSplitForAccumulator_GaugesRoutedToPending(t *testing.T) {
	in := []*dto.MetricFamily{
		gaugeFamily("segments_uploads_channel_size",
			gaugeMetric(2, "egress_id", "EG_aaa"),
		),
		counterFamily("uploads", counterMetric(3, "type", "file", "status", "success")),
	}

	acc, pending := splitForAccumulator(in)
	require.Len(t, acc, 1)
	require.Equal(t, "uploads", acc[0].GetName())
	require.Len(t, pending, 1)
	require.Equal(t, "segments_uploads_channel_size", pending[0].GetName())
}

func TestSplitForAccumulator_EgressIDMetricsRoutedToPending(t *testing.T) {
	in := []*dto.MetricFamily{
		counterFamily("hypothetical_per_egress_counter",
			counterMetric(1, "egress_id", "EG_aaa"),
		),
		counterFamily("uploads",
			counterMetric(3, "type", "file", "status", "success"),
			counterMetric(1, "egress_id", "EG_aaa", "type", "file"),
		),
	}

	acc, pending := splitForAccumulator(in)

	// 'uploads' splits: one metric (no egress_id) → accumulator; one (with
	// egress_id) → pending. 'hypothetical_per_egress_counter' is entirely
	// per-egress → pending.
	require.Len(t, acc, 1)
	require.Equal(t, "uploads", acc[0].GetName())
	require.Len(t, acc[0].Metric, 1)
	require.Equal(t, 3.0, acc[0].Metric[0].Counter.GetValue())

	pendingByName := map[string]*dto.MetricFamily{}
	for _, f := range pending {
		pendingByName[f.GetName()] = f
	}
	require.Contains(t, pendingByName, "hypothetical_per_egress_counter")
	require.Contains(t, pendingByName, "uploads")
	require.Len(t, pendingByName["uploads"].Metric, 1)
	require.Equal(t, 1.0, pendingByName["uploads"].Metric[0].Counter.GetValue())
}

func TestMergeFamilies_AccumulatorNotMutated(t *testing.T) {
	// First "scrape": accumulator starts empty, fold in a finished handler.
	accumulator, err := mergeFamilies([]*dto.MetricFamily{
		counterFamily("uploads", counterMetric(4, "type", "file", "status", "success")),
	})
	require.NoError(t, err)

	// Second "scrape": re-merge accumulator with a live handler. mergeFamilies
	// should not mutate accumulator's metric values.
	beforeValue := accumulator[0].Metric[0].Counter.GetValue()
	_, err = mergeFamilies(append(accumulator, counterFamily("uploads",
		counterMetric(10, "type", "file", "status", "success"))))
	require.NoError(t, err)
	afterValue := accumulator[0].Metric[0].Counter.GetValue()
	require.Equal(t, beforeValue, afterValue, "mergeFamilies must not mutate inputs")
}

// ---- helpers for the remaining metric types ----

func summaryFamily(name string, metrics ...*dto.Metric) *dto.MetricFamily {
	n := name
	t := dto.MetricType_SUMMARY
	return &dto.MetricFamily{Name: &n, Type: &t, Metric: metrics}
}

func summaryMetric(count uint64, sum float64, labels ...string) *dto.Metric {
	sc := count
	ss := sum
	// Include a quantile so we can assert it gets dropped on clone.
	q := 0.5
	qv := 1.0
	return &dto.Metric{
		Label: labelPairs(labels...),
		Summary: &dto.Summary{
			SampleCount: &sc,
			SampleSum:   &ss,
			Quantile:    []*dto.Quantile{{Quantile: &q, Value: &qv}},
		},
	}
}

func untypedFamily(name string, metrics ...*dto.Metric) *dto.MetricFamily {
	n := name
	t := dto.MetricType_UNTYPED
	return &dto.MetricFamily{Name: &n, Type: &t, Metric: metrics}
}

func untypedMetric(value float64, labels ...string) *dto.Metric {
	v := value
	return &dto.Metric{
		Label:   labelPairs(labels...),
		Untyped: &dto.Untyped{Value: &v},
	}
}

func familyWithHelpUnit(f *dto.MetricFamily, help, unit string) *dto.MetricFamily {
	if help != "" {
		h := help
		f.Help = &h
	}
	if unit != "" {
		u := unit
		f.Unit = &u
	}
	return f
}

// ---- summary / untyped aggregation ----

func TestMergeFamilies_SummarySumsCountAndSumDropsQuantiles(t *testing.T) {
	in := []*dto.MetricFamily{
		summaryFamily("latency", summaryMetric(2, 10, "type", "file")),
		summaryFamily("latency", summaryMetric(3, 20, "type", "file")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Metric, 1)
	s := out[0].Metric[0].Summary
	require.Equal(t, uint64(5), s.GetSampleCount())
	require.Equal(t, 30.0, s.GetSampleSum())
	// Quantiles must not survive the clone.
	require.Empty(t, s.Quantile)
}

func TestMergeFamilies_UntypedSumsValues(t *testing.T) {
	in := []*dto.MetricFamily{
		untypedFamily("custom_total", untypedMetric(2.5, "k", "v")),
		untypedFamily("custom_total", untypedMetric(7.5, "k", "v")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Equal(t, 10.0, out[0].Metric[0].Untyped.GetValue())
}

// ---- histogram edge cases ----

func TestMergeFamilies_HistogramBucketAsymmetric(t *testing.T) {
	// dst has bucket 50 that src doesn't have; src has bucket 100 that dst
	// doesn't have. Only the matched buckets are merged; the dst-only bucket
	// keeps its original cumulative count, and the src-only bucket is dropped
	// (handlers are assumed to register identical bucket boundaries).
	in := []*dto.MetricFamily{
		histogramFamily("response_time", histogramMetric(1, 5, map[float64]uint64{
			10: 1, 50: 1,
		}, "type", "file")),
		histogramFamily("response_time", histogramMetric(2, 15, map[float64]uint64{
			10: 1, 100: 2,
		}, "type", "file")),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	h := out[0].Metric[0].Histogram
	require.Equal(t, uint64(3), h.GetSampleCount())
	require.Equal(t, 20.0, h.GetSampleSum())

	byBound := map[float64]uint64{}
	for _, b := range h.Bucket {
		byBound[b.GetUpperBound()] = b.GetCumulativeCount()
	}
	require.Equal(t, uint64(2), byBound[10], "bucket 10 present on both sides — should sum")
	require.Equal(t, uint64(1), byBound[50], "bucket 50 only in dst — should pass through unchanged")
	require.NotContains(t, byBound, 100.0, "bucket 100 only in src — should not appear in dst")
}

// ---- family ordering, help / unit propagation ----

func TestMergeFamilies_MultiFamilySortedAndPreservesHelpUnit(t *testing.T) {
	in := []*dto.MetricFamily{
		familyWithHelpUnit(
			counterFamily("zzz_uploads", counterMetric(1, "type", "file")),
			"upload count", "1",
		),
		familyWithHelpUnit(
			counterFamily("aaa_bytes", counterMetric(100, "kind", "video")),
			"bytes sent", "bytes",
		),
	}

	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 2)
	// Output families are sorted by name.
	require.Equal(t, "aaa_bytes", out[0].GetName())
	require.Equal(t, "zzz_uploads", out[1].GetName())
	// Help and Unit from the first occurrence are preserved.
	require.Equal(t, "bytes sent", out[0].GetHelp())
	require.Equal(t, "bytes", out[0].GetUnit())
	require.Equal(t, "upload count", out[1].GetHelp())
	require.Equal(t, "1", out[1].GetUnit())
}

// ---- label-set keys ----

func TestLabelSetKey_NoLabels(t *testing.T) {
	require.Equal(t, "", labelSetKey(nil))
	require.Equal(t, "", labelSetKey([]*dto.LabelPair{}))
}

func TestLabelSetKey_OrderIndependent(t *testing.T) {
	// labelSetKey must produce the same key regardless of label declaration
	// order — otherwise distinct-handler scrapes wouldn't merge.
	a := labelSetKey(labelPairs("type", "file", "status", "success"))
	b := labelSetKey(labelPairs("status", "success", "type", "file"))
	require.Equal(t, a, b)
}

func TestMergeFamilies_LabelOrderIndependentMerging(t *testing.T) {
	// Same labels in different declaration orders should merge.
	in := []*dto.MetricFamily{
		counterFamily("uploads", counterMetric(3, "type", "file", "status", "success")),
		counterFamily("uploads", counterMetric(5, "status", "success", "type", "file")),
	}
	out, err := mergeFamilies(in)
	require.NoError(t, err)
	require.Len(t, out, 1)
	require.Len(t, out[0].Metric, 1)
	require.Equal(t, 8.0, out[0].Metric[0].Counter.GetValue())
}

// ---- clone isolation across types ----

func TestMergeFamilies_HistogramCloneIsolation(t *testing.T) {
	// Once a histogram is in the accumulator, merging more does not mutate
	// the original's bucket counters in place — the accumulator can be
	// re-merged repeatedly without drift.
	in := []*dto.MetricFamily{
		histogramFamily("response_time", histogramMetric(2, 30, map[float64]uint64{10: 1, 100: 2}, "type", "file")),
	}
	accumulator, err := mergeFamilies(in)
	require.NoError(t, err)

	originalCount := accumulator[0].Metric[0].Histogram.GetSampleCount()
	originalBucket10 := map[float64]uint64{}
	for _, b := range accumulator[0].Metric[0].Histogram.Bucket {
		originalBucket10[b.GetUpperBound()] = b.GetCumulativeCount()
	}

	more := []*dto.MetricFamily{histogramFamily("response_time", histogramMetric(5, 70, map[float64]uint64{10: 3, 100: 5}, "type", "file"))}
	_, err = mergeFamilies(append(accumulator, more...))
	require.NoError(t, err)

	require.Equal(t, originalCount, accumulator[0].Metric[0].Histogram.GetSampleCount(),
		"sample_count on prior merge output must not be mutated")
	for _, b := range accumulator[0].Metric[0].Histogram.Bucket {
		require.Equal(t, originalBucket10[b.GetUpperBound()], b.GetCumulativeCount(),
			"bucket %f on prior merge output must not be mutated", b.GetUpperBound())
	}
}

// ---- accumulateInto nil-typed safety ----

func TestAccumulateInto_NilFieldsAreNoOps(t *testing.T) {
	// If either side lacks the type-specific field, accumulateInto returns
	// without mutating. This guards against degenerate metric texts that
	// declare a type but omit the value.
	dst := &dto.Metric{Counter: &dto.Counter{Value: float64Ptr(3)}}
	require.NoError(t, accumulateInto(dto.MetricType_COUNTER, dst, &dto.Metric{}))
	require.Equal(t, 3.0, dst.Counter.GetValue())

	dstU := &dto.Metric{Untyped: &dto.Untyped{Value: float64Ptr(3)}}
	require.NoError(t, accumulateInto(dto.MetricType_UNTYPED, dstU, &dto.Metric{}))
	require.Equal(t, 3.0, dstU.Untyped.GetValue())

	dstH := &dto.Metric{Histogram: &dto.Histogram{
		SampleCount: uint64Ptr(2), SampleSum: float64Ptr(10),
	}}
	require.NoError(t, accumulateInto(dto.MetricType_HISTOGRAM, dstH, &dto.Metric{}))
	require.Equal(t, uint64(2), dstH.Histogram.GetSampleCount())

	dstS := &dto.Metric{Summary: &dto.Summary{
		SampleCount: uint64Ptr(2), SampleSum: float64Ptr(10),
	}}
	require.NoError(t, accumulateInto(dto.MetricType_SUMMARY, dstS, &dto.Metric{}))
	require.Equal(t, uint64(2), dstS.Summary.GetSampleCount())
}

func float64Ptr(v float64) *float64 { return &v }
func uint64Ptr(v uint64) *uint64    { return &v }

// ---- splitForAccumulator edge cases ----

func TestSplitForAccumulator_AllAccumulableEmptyPending(t *testing.T) {
	in := []*dto.MetricFamily{
		counterFamily("uploads", counterMetric(3, "type", "file", "status", "success")),
		histogramFamily("response_time", histogramMetric(2, 30, map[float64]uint64{10: 1}, "type", "file")),
	}
	acc, pending := splitForAccumulator(in)
	require.Len(t, acc, 2)
	require.Empty(t, pending)
}

func TestSplitForAccumulator_AllPendingEmptyAccumulator(t *testing.T) {
	in := []*dto.MetricFamily{
		gaugeFamily("queue_depth", gaugeMetric(2, "egress_id", "EG_aaa")),
		counterFamily("hypothetical_per_egress_counter", counterMetric(1, "egress_id", "EG_aaa")),
	}
	acc, pending := splitForAccumulator(in)
	require.Empty(t, acc)
	require.Len(t, pending, 2)
}

func TestSplitForAccumulator_Empty(t *testing.T) {
	acc, pending := splitForAccumulator(nil)
	require.Empty(t, acc)
	require.Empty(t, pending)
}

func TestSplitForAccumulator_PreservesHelpAndUnitInBothOutputs(t *testing.T) {
	// A mixed family with help/unit metadata is split into two new families;
	// both must carry the original metadata so the merged scrape output keeps
	// it on both the accumulator-side and the pending-side appearance.
	in := []*dto.MetricFamily{
		familyWithHelpUnit(
			counterFamily("mixed",
				counterMetric(3, "type", "file"),                 // → accumulable
				counterMetric(1, "egress_id", "EG_aaa", "k", "v"), // → pending
			),
			"a mixed counter", "1",
		),
	}
	acc, pending := splitForAccumulator(in)
	require.Len(t, acc, 1)
	require.Len(t, pending, 1)
	require.Equal(t, "a mixed counter", acc[0].GetHelp())
	require.Equal(t, "1", acc[0].GetUnit())
	require.Equal(t, "a mixed counter", pending[0].GetHelp())
	require.Equal(t, "1", pending[0].GetUnit())
}

func TestSplitForAccumulator_DoesNotMutateInputFamily(t *testing.T) {
	// splitForAccumulator constructs new families via familyWithMetrics and
	// must not touch the input's Metric slice — important because
	// deserializeMetrics' output may be reused or referenced elsewhere.
	mixed := counterFamily("mixed",
		counterMetric(3, "type", "file"),
		counterMetric(1, "egress_id", "EG_aaa"),
	)
	before := len(mixed.Metric)
	_, _ = splitForAccumulator([]*dto.MetricFamily{mixed})
	require.Equal(t, before, len(mixed.Metric), "input family Metric slice must not be modified")
}
