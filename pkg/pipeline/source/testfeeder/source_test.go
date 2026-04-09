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
// media-samples directory at the repo root.
func mediaSamplesDir(t *testing.T) string {
	if dir := os.Getenv("MEDIA_SAMPLES_DIR"); dir != "" {
		return dir
	}
	dir := repoRoot(t)
	samples := filepath.Join(dir, "media-samples")
	if _, err := os.Stat(samples); err != nil {
		t.Skipf("media-samples not found at %s (set MEDIA_SAMPLES_DIR)", samples)
	}
	return samples
}

// repoRoot walks up from the current file's directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	require.NoError(t, err)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod found)")
		}
		dir = parent
	}
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

// TestAsymmetricAudioRates tests that the non-live pipeline correctly handles
// two audio sources producing at different rates. One track pushes at full speed
// while the other is throttled to roughly realtime (20ms sleep per 20ms Opus
// frame). The audiomixer should wait for the slow source via backpressure — no
// data loss, valid output.
func TestAsymmetricAudioRates(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-asymmetric.ogg"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeOGG)
	conf.Latency.AudioMixerLatency = 2750 * time.Millisecond

	gst.Init(nil)

	// Open the slow reader manually so we can wrap it.
	// 20ms delay per frame ≈ realtime playback speed, roughly 2x slower than
	// the fast track which pushes as fast as the pipeline accepts.
	// Using a different audio file so both tracks are audibly distinguishable
	// in the output.
	slowInner, err := testfeeder.NewOGGReader(filepath.Join(samples, "avsync_minmotion_livekit_audio_48k_120s.ogg"))
	require.NoError(t, err)
	slowReader := testfeeder.NewSlowReader(slowInner, 20*time.Millisecond)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			TrackID:     "fast_track",
			Path:        filepath.Join(samples, "SolLevante.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
		{
			TrackID:     "slow_track",
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
			Reader:      slowReader,
		},
	})
	require.NoError(t, err)

	require.Len(t, conf.AudioTracks, 2)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)
	t.Logf("slow track (120s audio at realtime) gates the pipeline, got %v wall clock", elapsed)

	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}

// TestMixerStallsOnGap proves that the non-live audiomixer blocks forever when
// one track stops producing data without sending EOS or a GAP event. This
// demonstrates why non-live sources must detect gaps in their input data and
// inject GAP events.
func TestMixerStallsOnGap(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-stall.ogg"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeOGG)
	conf.Latency.AudioMixerLatency = 2750 * time.Millisecond

	gst.Init(nil)

	// The stalling reader delivers 50 frames (~1s of Opus) then blocks forever,
	// simulating a track with a gap in its recording.
	stallingInner, err := testfeeder.NewOGGReader(filepath.Join(samples, "avsync_minmotion_livekit_audio_48k_120s.ogg"))
	require.NoError(t, err)
	stallingReader := testfeeder.NewStallingReader(stallingInner, 50)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			TrackID:     "normal_track",
			Path:        filepath.Join(samples, "SolLevante.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
		{
			TrackID:     "stalling_track",
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
			Reader:      stallingReader,
		},
	})
	require.NoError(t, err)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	// Run the pipeline in a goroutine since it will stall.
	done := make(chan *livekit.EgressInfo, 1)
	go func() {
		done <- ctrl.Run(ctx)
	}()

	// Wait for the stalling reader to confirm it has stopped producing data.
	select {
	case <-stallingReader.Stalled():
		t.Log("stalling reader has stopped producing data after 50 frames")
	case <-time.After(30 * time.Second):
		t.Fatal("timed out waiting for stalling reader to stall")
	}

	// The pipeline should NOT complete within 5 seconds — the mixer is blocked
	// waiting for the stalling track.
	select {
	case info := <-done:
		t.Fatalf("pipeline should have stalled but completed with status: %s", info.Status)
	case <-time.After(5 * time.Second):
		t.Log("CONFIRMED: pipeline is stalled — mixer is blocked waiting for missing data")
	}

	// Clean up: unblock the stalling reader so the pipeline can shut down.
	stallingReader.Unblock()

	select {
	case info := <-done:
		t.Logf("pipeline finished after unblock, status: %s", info.Status)
	case <-time.After(30 * time.Second):
		t.Fatal("pipeline did not finish after unblocking stalling reader")
	}
}

// TestGapEventUnblocksMixer proves that sending a GAP event on a track allows
// the audiomixer to proceed with silence instead of blocking forever. This is
// the fix for the stall demonstrated in TestMixerStallsOnGap.
//
// Track 1: full SolLevante.ogg (~28s), pushes at full speed.
// Track 2: 50 frames (~1s) of audio, then a 10s GAP event, then continues.
//
// The pipeline should complete quickly (faster than realtime) without stalling.
func TestGapEventUnblocksMixer(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-gap-event.ogg"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeOGG)
	conf.Latency.AudioMixerLatency = 2750 * time.Millisecond

	gst.Init(nil)

	// After 50 frames (~1s), inject a 10s GAP event, then continue with
	// remaining frames. The mixer should fill silence for track 2 during
	// the gap and keep mixing track 1.
	gappingInner, err := testfeeder.NewOGGReader(filepath.Join(samples, "avsync_minmotion_livekit_audio_48k_120s.ogg"))
	require.NoError(t, err)
	gappingReader := testfeeder.NewGappingReader(gappingInner, 50, 10*time.Second)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			TrackID:     "normal_track",
			Path:        filepath.Join(samples, "SolLevante.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
		{
			TrackID:     "gapping_track",
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
			Reader:      gappingReader,
		},
	})
	require.NoError(t, err)

	require.Len(t, conf.AudioTracks, 2)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)

	// The pipeline should complete much faster than realtime — if it took
	// >15s, something is wrong (the gap event didn't unblock the mixer).
	require.Less(t, elapsed, 15*time.Second,
		"pipeline should complete faster than realtime; GAP event may not have worked")

	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}

// TestRoomCompositeAudioOnly tests the non-live pipeline with two Opus audio
// tracks mixed through audiomixer, simulating an audio-only room composite with
// two participants. Both tracks are decoded, mixed, and re-encoded to OGG/Opus.
func TestRoomCompositeAudioOnly(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-room-audio.ogg"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeOGG)
	conf.Latency.AudioMixerLatency = 2750 * time.Millisecond

	gst.Init(nil)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			TrackID:     "participant_1_audio",
			Path:        filepath.Join(samples, "SolLevante.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
		{
			TrackID:     "participant_2_audio",
			Path:        filepath.Join(samples, "avsync_minmotion_livekit_audio_48k_120s.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
	})
	require.NoError(t, err)

	require.True(t, conf.AudioEnabled)
	require.False(t, conf.VideoEnabled)
	require.False(t, conf.Live)
	require.Len(t, conf.AudioTracks, 2)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)

	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}

// TestAudioVideoMP4 tests the non-live pipeline with both an Opus audio track
// and an H.264 video track producing an MP4 file. This simulates a participant
// egress: audio goes through Opus→decode→AAC encode, video passes through as
// H.264.
func TestAudioVideoMP4(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-av.mp4"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeMP4)
	conf.VideoConfig = config.VideoConfig{
		VideoProfile: types.ProfileMain,
		Width:        1920,
		Height:       1080,
		Depth:        24,
		Framerate:    25,
		VideoBitrate: 3000,
	}
	conf.AudioOutCodec = types.MimeTypeAAC
	conf.AudioTranscoding = true

	gst.Init(nil)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			Path:        filepath.Join(samples, "avsync_minmotion_livekit_audio_48k_120s.ogg"),
			MimeType:    types.MimeTypeOpus,
			PayloadType: 111,
		},
		{
			Path:        filepath.Join(samples, "avsync_minmotion_livekit_video_1080p25_120s.h264"),
			MimeType:    types.MimeTypeH264,
			PayloadType: 96,
			FPS:         25,
		},
	})
	require.NoError(t, err)

	require.True(t, conf.AudioEnabled)
	require.True(t, conf.VideoEnabled)
	require.False(t, conf.Live)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)

	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}

// TestVideoOnlyH264MP4 tests the non-live pipeline with a single H.264 video
// track producing an MP4 file. No audio — just video appsrc → depay → mux → file.
func TestVideoOnlyH264MP4(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-video-h264.mp4"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeMP4)
	conf.VideoConfig = config.VideoConfig{
		VideoProfile: types.ProfileMain,
		Width:        1920,
		Height:       1080,
		Depth:        24,
		Framerate:    25,
		VideoBitrate: 3000,
	}

	gst.Init(nil)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			Path:        filepath.Join(samples, "avsync_minmotion_livekit_video_1080p25_120s.h264"),
			MimeType:    types.MimeTypeH264,
			PayloadType: 96,
			FPS:         25,
		},
	})
	require.NoError(t, err)

	require.True(t, conf.VideoEnabled)
	require.False(t, conf.Live)
	require.NotNil(t, conf.VideoTrack)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)

	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}

// TestVideoOnlyVP8WebM tests the non-live pipeline with a single VP8 video
// track producing a WebM file.
func TestVideoOnlyVP8WebM(t *testing.T) {
	samples := mediaSamplesDir(t)
	outputPath := "/tmp/testfeeder-video-vp8.webm"

	conf := newTestPipelineConfig(t, outputPath, types.OutputTypeWebM)
	conf.VideoConfig = config.VideoConfig{
		VideoProfile: types.ProfileMain,
		Width:        1920,
		Height:       1080,
		Depth:        24,
		Framerate:    24,
		VideoBitrate: 3000,
	}

	gst.Init(nil)

	src, err := testfeeder.NewTestSource(conf, []testfeeder.TrackDef{
		{
			Path:        filepath.Join(samples, "SolLevante-vp8.ivf"),
			MimeType:    types.MimeTypeVP8,
			PayloadType: 96,
		},
	})
	require.NoError(t, err)

	require.True(t, conf.VideoEnabled)
	require.False(t, conf.Live)
	require.NotNil(t, conf.VideoTrack)

	ctx := context.Background()
	ctrl, err := pipeline.NewWithSource(ctx, conf, src)
	require.NoError(t, err)

	src.SetCallbacks(ctrl.Callbacks())

	start := time.Now()
	info := ctrl.Run(ctx)
	elapsed := time.Since(start)

	t.Logf("pipeline completed in %v, status: %s", elapsed, info.Status)

	stat, err := os.Stat(outputPath)
	require.NoError(t, err, "output file should exist")
	require.Greater(t, stat.Size(), int64(0), "output file should not be empty")

	t.Logf("output file: %s (%d bytes)", outputPath, stat.Size())
}
