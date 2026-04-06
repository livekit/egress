// Copyright 2024 LiveKit, Inc.
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

//go:build integration

package testfeeder_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline"
	"github.com/livekit/egress/pkg/pipeline/source/testfeeder"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/observability/storageobs"
)

func init() {
	os.Setenv("GST_DEBUG", "audiomixer:6,aggregator:6,queue:5,audiotestsrc:5")
	logger.InitFromConfig(&logger.Config{Level: "debug"}, "testfeeder")
}

// mediaSamplesDir returns the path to the media-samples directory.
// Set MEDIA_SAMPLES_DIR env var to override, otherwise defaults to the
// cloud-egress media-samples relative to this repo.
func mediaSamplesDir(t *testing.T) string {
	if dir := os.Getenv("MEDIA_SAMPLES_DIR"); dir != "" {
		return dir
	}
	// Default: assume egress and cloud-egress are sibling dirs
	dir := filepath.Join("..", "..", "..", "..", "..", "cloud-egress", "media-samples")
	abs, err := filepath.Abs(dir)
	require.NoError(t, err)
	if _, err := os.Stat(abs); err != nil {
		t.Skipf("media-samples not found at %s (set MEDIA_SAMPLES_DIR)", abs)
	}
	return abs
}

// newTestPipelineConfig creates a minimal PipelineConfig suitable for pipeline
// construction. Track info is populated by TestSource; this sets up everything
// else the pipeline needs (latency, output config, etc.).
func newTestPipelineConfig(t *testing.T, outputPath string, outputType types.OutputType) *config.PipelineConfig {
	t.Helper()

	tmpDir := t.TempDir()

	p := &config.PipelineConfig{
		BaseConfig: config.BaseConfig{
			Logging: &logger.Config{Level: "debug"},
			Latency: config.LatencyConfig{
				PipelineLatency: 3 * time.Second,
			},
		},
		TmpDir:          tmpDir,
		Outputs:         make(map[types.EgressType][]config.OutputConfig),
		StorageReporter: storageobs.NewNoopProjectReporter(),
	}

	p.AudioConfig = config.AudioConfig{
		AudioBitrate:   128,
		AudioFrequency: 48000,
	}

	p.Info = &livekit.EgressInfo{
		EgressId:  "test-feeder-integration",
		Status:    livekit.EgressStatus_EGRESS_STARTING,
		StartedAt: time.Now().UnixNano(),
		UpdatedAt: time.Now().UnixNano(),
	}

	p.Outputs[types.EgressTypeFile] = []config.OutputConfig{
		&config.FileConfig{
			FileInfo:        &livekit.FileInfo{},
			LocalFilepath:   outputPath,
			StorageFilepath: outputPath,
			DisableManifest: true,
		},
	}
	p.GetFileConfig().OutputType = outputType

	return p
}

// TestAudioOnlyOGG tests the full non-live pipeline with a single Opus audio
// track producing an OGG file. This is the simplest case: one appsrc, one
// depayloader, one decoder, one encoder, one muxer, one file sink.
func TestAudioOnlyOGG(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-output.ogg"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeOGG)

	// Initialize GStreamer before creating source (appsrc needs it)
	gst.Init(nil)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			Path:        filepath.Join(samples, "SolLevante.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
	})
	require.NoError(t, err)

	// Verify config was populated
	require.True(t, conf.AudioEnabled)
	require.False(t, conf.Live)
	require.Len(t, conf.AudioTracks, 1)

	// Create pipeline via controller
	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	// Wire callbacks so feeders know when pipeline is ready
	src.SetCallbacks(ctrl.Callbacks())

	// Run pipeline to completion — this blocks until EOS.
	// The controller calls src.StartRecording() which starts the feeders.
	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)

	// Verify output file exists and has content
	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}
