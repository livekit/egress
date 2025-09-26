package builder

import (
	"strings"
	"sync"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/stretchr/testify/require"
)

var gstInitOnce sync.Once

func initGStreamer(t *testing.T) {
	t.Helper()
	gstInitOnce.Do(func() { gst.Init(nil) })
}

func TestNewMuxer_KnownMuxers(t *testing.T) {
	initGStreamer(t)

	for _, name := range []string{"oggmux", "avmux_ivf", "mp4mux", "webmmux", "mpegtsmux"} {
		t.Run(name, func(t *testing.T) {
			m, err := newMuxer(name)
			require.NoError(t, err)
			require.NotNil(t, m)
			require.NotNil(t, m.GetElement())
		})
	}
}

func TestNewMuxer_InvalidMuxer(t *testing.T) {
	initGStreamer(t)

	_, err := newMuxer("identity")
	require.Error(t, err)
	require.True(t, strings.Contains(err.Error(), "not a muxer"), "unexpected error: %v", err)
}

func TestNewMP3Muxer(t *testing.T) {
	initGStreamer(t)

	m, err := newMP3Muxer()
	require.NoError(t, err)
	require.NotNil(t, m.GetRequestPad("unused"))
}
