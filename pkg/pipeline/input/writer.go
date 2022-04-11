package input

import (
	"github.com/pion/rtp"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
	"time"
)

type appWriter struct {
	src         *app.Source
	runtime    time.Time
	runtimeSet bool
}

var Ready = false

func (w *appWriter) WriteRTP(pkt *rtp.Packet) error {
	if !Ready {
		return nil
	}

	// Set run time
	if !w.runtimeSet {
		w.runtime = time.Now()
		w.runtimeSet = true
	}

	p, err := pkt.Marshal()
	if err != nil {
		return err
	}

	// Parse packet timestamp
	ts := time.Unix(int64(pkt.Timestamp), 0)

	// Create buffer and set PTS
	b := gst.NewBufferFromBytes(p)
	d := ts.Sub(w.runtime)
	b.SetPresentationTimestamp(d)

	w.src.PushBuffer(b)
	return nil
}

func (w *appWriter) Close() error {
	return nil
}
