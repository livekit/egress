package input

import (
	"github.com/pion/rtp"
	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"
)

type appWriter struct {
	src *app.Source
}

var Ready = false

func (w *appWriter) WriteRTP(pkt *rtp.Packet) error {
	if !Ready {
		return nil
	}

	b, err := pkt.Marshal()
	if err != nil {
		return err
	}
	w.src.PushBuffer(gst.NewBufferFromBytes(b))
	return nil
}

func (w *appWriter) Close() error {
	return nil
}
