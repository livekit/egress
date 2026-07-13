package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
)

// fakeRedis is a minimal in-memory fake for testing heartbeat writes.
type fakeRedis struct {
	redis.Cmdable // embed to satisfy interface; unused methods will panic

	sets map[string]fakeEntry
	dels []string
}

type fakeEntry struct {
	val string
	ttl time.Duration
}

func newFakeRedis() *fakeRedis { return &fakeRedis{sets: map[string]fakeEntry{}} }

func (f *fakeRedis) Set(_ context.Context, key string, value interface{}, ttl time.Duration) *redis.StatusCmd {
	f.sets[key] = fakeEntry{val: fmt.Sprintf("%v", value), ttl: ttl}
	return nil
}

func (f *fakeRedis) Del(_ context.Context, keys ...string) *redis.IntCmd {
	f.dels = append(f.dels, keys...)
	return nil
}

func newTestServer(rc redis.Cmdable, max int32) *Server {
	s := &Server{
		conf: &config.ServiceConfig{
			BaseConfig: config.BaseConfig{},
		},
		rc: rc,
	}
	s.conf.MaxActiveRequests = max
	return s
}

// TestWriteHeartbeat_ImmediateAndFormat verifies that writeHeartbeat writes a
// correctly formatted "active/max" value with the right TTL.
func TestWriteHeartbeat_ImmediateAndFormat(t *testing.T) {
	fake := newFakeRedis()
	s := newTestServer(fake, 16)

	s.writeHeartbeat(context.Background(), "lk:egress:pod:test-pod")

	entry, ok := fake.sets["lk:egress:pod:test-pod"]
	require.True(t, ok, "key should have been written")
	assert.Equal(t, "0/16", entry.val)
	assert.Equal(t, egressHeartbeatTTL, entry.ttl)
}

// TestWriteHeartbeat_DefaultMax verifies that a zero MaxActiveRequests falls back to 16.
func TestWriteHeartbeat_DefaultMax(t *testing.T) {
	fake := newFakeRedis()
	s := newTestServer(fake, 0)

	s.writeHeartbeat(context.Background(), "lk:egress:pod:x")

	entry := fake.sets["lk:egress:pod:x"]
	assert.Equal(t, "0/16", entry.val)
}

// TestWriteHeartbeat_NilRC verifies that writeHeartbeat is a no-op when rc is nil.
func TestWriteHeartbeat_NilRC(t *testing.T) {
	s := newTestServer(nil, 16)
	// Should not panic.
	s.writeHeartbeat(context.Background(), "lk:egress:pod:x")
}

// TestStartHeartbeat_WritesImmediatelyAndDeletesOnCancel verifies that the
// heartbeat key is written on startup and deleted when context is cancelled.
func TestStartHeartbeat_WritesImmediatelyAndDeletesOnCancel(t *testing.T) {
	fake := newFakeRedis()
	s := newTestServer(fake, 16)

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		s.startHeartbeat(ctx)
	}()

	// Give the goroutine time to write the initial key.
	time.Sleep(50 * time.Millisecond)
	_, written := fake.sets["lk:egress:pod:unknown"]
	assert.True(t, written, "key should be written immediately at startup")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat goroutine did not exit after context cancel")
	}

	assert.Contains(t, fake.dels, "lk:egress:pod:unknown", "key should be deleted on shutdown")
}
