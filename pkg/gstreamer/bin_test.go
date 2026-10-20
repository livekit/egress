// Copyright 2026 LiveKit, Inc.
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

package gstreamer

import (
	"sync"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

type sinkRemovalFixture struct {
	pipeline *Pipeline
	parent   *Bin
	sinks    map[string]*Bin

	mu   sync.Mutex
	errs []error
}

// newSinkRemovalFixture builds the topology a stream sink has: a parent bin whose
// last element is a tee, with one child bin per output hanging off a tee request pad.
func newSinkRemovalFixture(t *testing.T, sinkNames ...string) *sinkRemovalFixture {
	t.Helper()
	gst.Init(nil)

	f := &sinkRemovalFixture{sinks: make(map[string]*Bin, len(sinkNames))}

	callbacks := &Callbacks{}
	callbacks.SetOnError(func(err error) {
		f.mu.Lock()
		f.errs = append(f.errs, err)
		f.mu.Unlock()
	})

	p, err := NewPipeline("sink-removal-test", 0, callbacks)
	require.NoError(t, err)
	f.pipeline = p

	parent := p.NewBin("parent")
	tee, err := gst.NewElementWithName("tee", "parent_tee")
	require.NoError(t, err)
	require.NoError(t, parent.AddElements(tee))
	require.NoError(t, p.AddSinkBin(parent))
	f.parent = parent

	for _, name := range sinkNames {
		sink := parent.NewBin(name)
		queue, err := gst.NewElementWithName("queue", "queue_"+name)
		require.NoError(t, err)
		fakeSink, err := gst.NewElementWithName("fakesink", "fakesink_"+name)
		require.NoError(t, err)
		require.NoError(t, sink.AddElements(queue, fakeSink))
		require.NoError(t, parent.AddSinkBin(sink))
		f.sinks[name] = sink
	}

	require.NoError(t, p.Link())
	p.UpgradeState(StateRunning)

	return f
}

// ghostPad returns the parent's ghost pad for a sink. The pad has to be captured
// before removal, which deletes it from the bin.
func (f *sinkRemovalFixture) ghostPad(t *testing.T, sinkName string) *gst.GhostPad {
	t.Helper()

	f.parent.mu.Lock()
	defer f.parent.mu.Unlock()

	pad, ok := f.parent.pads[sinkName]
	require.True(t, ok, "no ghost pad for %s", sinkName)
	return pad
}

func (f *sinkRemovalFixture) inPipeline(sinkName string) bool {
	_, err := f.pipeline.bin.GetElementByName(sinkName)
	return err == nil
}

func (f *sinkRemovalFixture) errors() []error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]error(nil), f.errs...)
}

// A sink bin is removed by a pad probe that fires on every buffer, event and query
// crossing the parent's ghost pad. Each firing must find the bin already detached and
// do nothing; removing it twice makes gst_bin_remove fail on an element the pipeline
// no longer owns, which used to fail the whole egress and take the healthy sinks with it.
func TestRemoveSinkBinDetachesOnce(t *testing.T) {
	f := newSinkRemovalFixture(t, "failing_sink", "healthy_sink")
	pad := f.ghostPad(t, "failing_sink")

	require.NoError(t, f.parent.RemoveSinkBin("failing_sink"))

	for i := 0; i < 3; i++ {
		pad.Query(gst.NewLatencyQuery())
	}

	require.False(t, f.inPipeline("failing_sink"), "probe never detached the sink bin")
	require.True(t, f.inPipeline("healthy_sink"), "removing one sink detached the other")
	require.Empty(t, f.errors(), "sink removal reported an error to the pipeline")
}

// Removing the last sink follows the same path, with no sibling left to protect.
func TestRemoveOnlySinkBinDetachesOnce(t *testing.T) {
	f := newSinkRemovalFixture(t, "only_sink")
	pad := f.ghostPad(t, "only_sink")

	require.NoError(t, f.parent.RemoveSinkBin("only_sink"))

	for i := 0; i < 3; i++ {
		pad.Query(gst.NewLatencyQuery())
	}

	require.False(t, f.inPipeline("only_sink"), "probe never detached the sink bin")
	require.Empty(t, f.errors(), "sink removal reported an error to the pipeline")
}

// RemoveSinkBin is a no-op for a name the bin does not hold, and leaves the
// remaining sinks alone.
func TestRemoveUnknownSinkBin(t *testing.T) {
	f := newSinkRemovalFixture(t, "only_sink")

	require.NoError(t, f.parent.RemoveSinkBin("missing_sink"))

	require.True(t, f.inPipeline("only_sink"))
	require.Empty(t, f.errors())
}
