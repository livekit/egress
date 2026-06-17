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

	out := mergeFamilies(in)
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

	out := mergeFamilies(in)
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

	out := mergeFamilies(in)
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

	out := mergeFamilies(in)
	require.Len(t, out, 1)
	require.Equal(t, 7.0, out[0].Metric[0].Counter.GetValue())
}

func TestMergeFamilies_AccumulatorNotMutated(t *testing.T) {
	// First "scrape": accumulator starts empty, fold in a finished handler.
	accumulator := mergeFamilies([]*dto.MetricFamily{
		counterFamily("uploads", counterMetric(4, "type", "file", "status", "success")),
	})

	// Second "scrape": re-merge accumulator with a live handler. mergeFamilies
	// should not mutate accumulator's metric values.
	beforeValue := accumulator[0].Metric[0].Counter.GetValue()
	_ = mergeFamilies(append(accumulator, counterFamily("uploads",
		counterMetric(10, "type", "file", "status", "success"))))
	afterValue := accumulator[0].Metric[0].Counter.GetValue()
	require.Equal(t, beforeValue, afterValue, "mergeFamilies must not mutate inputs")
}
