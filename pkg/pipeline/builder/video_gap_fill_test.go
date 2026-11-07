package builder

import (
	"fmt"
	"testing"

	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/video"
	"github.com/stretchr/testify/require"
)

func TestNewBlackFrameMatchesVideoInfoLayout(t *testing.T) {
	// the frame carries no video meta, so its size must be the one gst_video_info_set_format derives from the caps,
	// padding included for odd and non-multiple-of-four sizes
	initGStreamer(t)
	for _, size := range [][2]int{{64, 64}, {1280, 720}, {641, 361}, {1279, 719}, {2, 2}, {1, 1}} {
		t.Run(fmt.Sprintf("%dx%d", size[0], size[1]), func(t *testing.T) {
			caps := gst.NewCapsFromString(fmt.Sprintf("video/x-raw,format=I420,width=%d,height=%d,framerate=30/1", size[0], size[1]))
			info := video.NewInfo().FromCaps(caps)

			buf := newBlackFrame(caps)
			require.NotNil(t, buf)
			require.Equal(t, info.Size(), buf.GetSize())

			mapped := buf.Map(gst.MapRead)
			defer buf.Unmap()
			data := mapped.Bytes()
			require.EqualValues(t, 16, data[0], "luma is not limited-range black")
			require.EqualValues(t, 128, data[len(data)-1], "chroma is not neutral")
		})
	}
}

func TestNewBlackFrameRefusesOtherFormats(t *testing.T) {
	initGStreamer(t)
	caps := gst.NewCapsFromString("video/x-raw,format=NV12,width=64,height=64,framerate=30/1")
	require.Nil(t, newBlackFrame(caps))
}
