package builder

import (
	"fmt"

	"github.com/tinyzimmer/go-gst/gst"
	"github.com/tinyzimmer/go-gst/gst/app"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
)

type pad interface {
	Link(*gst.Pad) gst.PadLinkReturn
}

func LinkPads(src string, srcPad pad, sink string, sinkPad *gst.Pad) error {
	if srcPad == nil {
		return errors.ErrPadLinkFailed(src, sink, fmt.Sprintf("missing %s pad", src))
	}
	if sinkPad == nil {
		return errors.ErrPadLinkFailed(src, sink, fmt.Sprintf("missing %s pad", sink))
	}
	if linkReturn := srcPad.Link(sinkPad); linkReturn != gst.PadLinkOK {
		return errors.ErrPadLinkFailed(src, sink, linkReturn.String())
	}
	return nil
}

func BuildQueue(name string, leaky bool) (*gst.Element, error) {
	queue, err := gst.NewElementWithName("queue", name)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	// max-size-time sets the maximum latency for the pipeline - set to 1s higher than source latency
	if err = queue.SetProperty("max-size-time", config.MaxPipelineLatency); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-bytes", uint(0)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if err = queue.SetProperty("max-size-buffers", uint(0)); err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}
	if leaky {
		queue.SetArg("leaky", "downstream")
	}

	return queue, nil
}

func GetSrcPad(elements []*gst.Element) *gst.Pad {
	return elements[len(elements)-1].GetStaticPad("src")
}

func UpdateAppSrc(src *app.Source) error {
	src.Element.SetArg("format", "time")
	if err := src.Element.SetProperty("is-live", true); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := src.Element.SetProperty("min-latency", int64(config.MinSDKLatency)); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	if err := src.Element.SetProperty("emit-signals", false); err != nil {
		return errors.ErrGstPipelineError(err)
	}
	return nil
}
