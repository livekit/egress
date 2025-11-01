package builder

import (
	"github.com/go-gst/go-gst/gst"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

// PTSFixer wraps a gst element and restores missing PTS values on its src pad
// so downstream elements observe a monotonic timeline even when upstream elements
// emit GST_CLOCK_TIME_NONE buffers (e.g. due to baseparse bugs).
type ptsFixer struct {
	*gst.Element
	pad     *gst.Pad
	probe   uint64
	last    uint64
	ptsSeen bool
	log     logger.Logger
}

func newPTSFixer(elementName, context string) (*ptsFixer, error) {
	element, err := gst.NewElement(elementName)
	if err != nil {
		return nil, errors.ErrGstPipelineError(err)
	}

	pad := element.GetStaticPad("src")
	if pad == nil {
		element.Unref()
		return nil, errors.ErrGstPipelineError(newMissingPadError(elementName, "src"))
	}

	fixer := &ptsFixer{
		Element: element,
		pad:     pad,
		log:     logger.GetLogger().WithValues("component", "pts_fixer", "context", context, "element", elementName),
	}
	fixer.probe = pad.AddProbe(gst.PadProbeTypeBuffer, fixer.onBuffer)

	return fixer, nil
}

func (f *ptsFixer) onBuffer(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
	buf := info.GetBuffer()
	if buf == nil {
		return gst.PadProbeOK
	}

	pts := buf.PresentationTimestamp()
	if pts == gst.ClockTimeNone {
		if !f.ptsSeen {
			return gst.PadProbeOK
		}

		restored := gst.ClockTime(f.last)
		buf.SetPresentationTimestamp(restored)
		f.log.Debugw("restored missing pts from previous buffer", "pts", restored)

		return gst.PadProbeOK
	}

	f.last = uint64(pts)
	f.ptsSeen = true
	return gst.PadProbeOK
}
