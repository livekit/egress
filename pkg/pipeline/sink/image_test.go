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

package sink

import (
	"fmt"
	"os"
	"path"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/gstreamer"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/protocol/livekit"
)

func newTestImageSink(t *testing.T, capacity int) (*ImageSink, chan error) {
	u, err := uploader.New(nil, nil, nil, nil, nil)
	require.NoError(t, err)

	errCh := make(chan error, 10)
	callbacks := &gstreamer.Callbacks{}
	callbacks.SetOnError(func(err error) { errCh <- err })

	return &ImageSink{
		Uploader: u,
		ImageConfig: &config.ImageConfig{
			ImagesInfo:  &livekit.ImagesInfo{},
			LocalDir:    t.TempDir(),
			StorageDir:  t.TempDir(),
			ImagePrefix: "img",
			ImageSuffix: livekit.ImageFileSuffix_IMAGE_SUFFIX_INDEX,
		},
		conf:          &config.PipelineConfig{},
		callbacks:     callbacks,
		createdImages: make(chan *imageUpdate, capacity),
	}, errCh
}

func requireNewImage(t *testing.T, s *ImageSink, filepath string) {
	done := make(chan error, 1)
	go func() {
		done <- s.NewImage(filepath, uint64(time.Now().UnixNano()))
	}()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("NewImage blocked")
	}
}

func requireClose(t *testing.T, s *ImageSink) {
	closed := make(chan struct{})
	go func() {
		require.NoError(t, s.Close())
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("Close blocked")
	}
}

// An upload failure must fail the egress exactly once, and the consumer must
// keep accepting images so the producer (the pipeline's bus thread) never blocks.
func TestImageSinkDrainsAfterUploadFailure(t *testing.T) {
	s, errCh := newTestImageSink(t, 2)
	require.NoError(t, s.Start())

	// no local file exists, so handleNewImage fails on upload
	requireNewImage(t, s, path.Join(s.LocalDir, "img_00001.jpg"))

	select {
	case err := <-errCh:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("OnError not called after upload failure")
	}

	// the consumer must keep draining well past channel capacity; sends may
	// transiently report a full queue but must never block
	for i := 2; i <= 12; i++ {
		fp := path.Join(s.LocalDir, fmt.Sprintf("img_%05d.jpg", i))
		require.Eventually(t, func() bool {
			err := s.NewImage(fp, uint64(time.Now().UnixNano()))
			if err != nil {
				require.ErrorContains(t, err, "upload queue full")
				return false
			}
			return true
		}, 3*time.Second, 10*time.Millisecond, "image %d never accepted", i)
	}

	requireClose(t, s)

	select {
	case err := <-errCh:
		t.Fatalf("OnError called more than once: %v", err)
	default:
	}
}

func TestImageSinkNewImageQueueFull(t *testing.T) {
	s, _ := newTestImageSink(t, 1)
	// consumer not started: first send fills the buffer, second must fail fast

	require.NoError(t, s.NewImage(path.Join(s.LocalDir, "img_00001.jpg"), 0))

	err := s.NewImage(path.Join(s.LocalDir, "img_00002.jpg"), 0)
	require.Error(t, err)
	require.ErrorContains(t, err, "upload queue full")
}

func TestImageSinkCloseDrainsPendingUploads(t *testing.T) {
	// the default local storage backend resolves storage paths against the
	// working directory at construction time
	t.Chdir(t.TempDir())

	s, errCh := newTestImageSink(t, 4)
	s.StorageDir = "images-out"

	filenames := []string{"img_00001.jpg", "img_00002.jpg"}
	for _, name := range filenames {
		require.NoError(t, os.WriteFile(path.Join(s.LocalDir, name), []byte("jpeg"), 0o644))
	}

	require.NoError(t, s.Start())
	for _, name := range filenames {
		requireNewImage(t, s, path.Join(s.LocalDir, name))
	}

	requireClose(t, s)

	require.EqualValues(t, len(filenames), s.ImagesInfo.ImageCount)
	for _, name := range filenames {
		_, err := os.Stat(path.Join(s.StorageDir, name))
		require.NoError(t, err, "image not uploaded to storage")
		_, err = os.Stat(path.Join(s.LocalDir, name))
		require.True(t, os.IsNotExist(err), "local image not removed after upload")
	}

	select {
	case err := <-errCh:
		t.Fatalf("unexpected error: %v", err)
	default:
	}
}

// Regression: a capture interval of exactly 3600s used to yield a single-slot
// queue, which the startup frame burst overflows, failing the egress.
func TestImageQueueCapacity(t *testing.T) {
	require.Equal(t, 360, imageQueueCapacity(60, 10))
	require.Equal(t, 60, imageQueueCapacity(60, 60))
	require.Equal(t, minPendingUploads, imageQueueCapacity(60, 3600))
	require.Equal(t, minPendingUploads, imageQueueCapacity(60, 7200))
	require.Equal(t, minPendingUploads, imageQueueCapacity(0, 10))
}
