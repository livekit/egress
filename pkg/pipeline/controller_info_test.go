package pipeline

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/livekit"
)

// Handler RPCs marshal InfoSnapshot from their own goroutines while the pipeline, sinks and timers
// keep writing Info. Every snapshot must encode with a stable size, and a returned snapshot must
// never change afterwards.
func TestInfoSnapshotUnderConcurrentWrites(t *testing.T) {
	c := &Controller{
		PipelineConfig: &config.PipelineConfig{
			Info: &livekit.EgressInfo{
				EgressId:    "EG_test",
				Status:      livekit.EgressStatus_EGRESS_ACTIVE,
				FileResults: []*livekit.FileInfo{{Filename: "out.mp4"}},
			},
		},
	}

	stop := make(chan struct{})
	var wg sync.WaitGroup
	writer := func(fn func(i int64)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := int64(1); ; i++ {
				select {
				case <-stop:
					return
				default:
					c.updateInfo(func() { fn(i) })
				}
			}
		}()
	}

	// Close(): end time and final status
	writer(func(i int64) {
		fileInfo := c.Info.FileResults[0]
		fileInfo.EndedAt = i
		fileInfo.Duration = i
		fileInfo.Location = "s3://bucket/out.mp4"
		c.Info.SetComplete()
	})
	// UpdateStream: repeated field grows and is reset
	writer(func(_ int64) {
		c.Info.StreamResults = append(c.Info.StreamResults, &livekit.StreamInfo{Url: "rtmp://x"})
		if len(c.Info.StreamResults) > 64 {
			c.Info.StreamResults = c.Info.StreamResults[:0]
		}
	})
	// stream callbacks: nested message writes
	writer(func(_ int64) {
		if len(c.Info.StreamResults) > 0 {
			c.Info.StreamResults[0].Retries++
			c.Info.StreamResults[0].LastRetryAt = int64(c.Info.StreamResults[0].Retries)
		}
	})

	deadline := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(deadline) {
		snap := c.InfoSnapshot()

		size := proto.Size(snap)
		first, err := proto.Marshal(snap)
		require.NoError(t, err)
		require.Len(t, first, size)

		second, err := proto.Marshal(snap)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Equal(t, "EG_test", snap.EgressId)

		// each writer sets these fields together under the lock, so a snapshot never sees them apart
		fileInfo := snap.FileResults[0]
		require.Equal(t, fileInfo.EndedAt, fileInfo.Duration)
		for _, stream := range snap.StreamResults {
			require.Equal(t, stream.LastRetryAt, int64(stream.Retries))
		}
	}
	close(stop)
	wg.Wait()
}
