package gstreamer

import (
	"fmt"
	"sync"
	"time"

	"github.com/go-gst/go-gst/gst"
)

type segSnap struct {
	format      gst.Format
	rate        float64
	appliedRate float64
	flags       uint32
	start       gst.ClockTime
	stop        gst.ClockTime
	timeCT      gst.ClockTime
	base        gst.ClockTime
	position    gst.ClockTime
}

func (s segSnap) String() string {
	ct := func(x gst.ClockTime) string {
		if x == gst.ClockTimeNone {
			return "NONE"
		}
		return time.Duration(x).String()
	}
	return fmt.Sprintf("fmt=%s rate=%.6f ar=%.6f flags=0x%x start=%s stop=%s time=%s base=%s pos=%s",
		s.format.String(), s.rate, s.appliedRate, s.flags,
		ct(s.start), ct(s.stop), ct(s.timeCT), ct(s.base), ct(s.position))
}

func (s segSnap) equal(b segSnap) bool {
	return s.format == b.format &&
		s.rate == b.rate &&
		s.appliedRate == b.appliedRate &&
		s.flags == b.flags &&
		s.start == b.start &&
		s.stop == b.stop &&
		s.timeCT == b.timeCT &&
		s.base == b.base &&
		s.position == b.position
}

func snapFromEvent(ev *gst.Event) (segSnap, bool) {
	// Adjust to your bindings if names differ:
	seg := ev.ParseSegment()
	if seg == nil {
		return segSnap{}, false
	}
	return segSnap{
		format:      seg.GetFormat(),
		rate:        seg.GetRate(),
		appliedRate: seg.GetAppliedRate(),
		flags:       uint32(seg.GetFlags()),
		start:       gst.ClockTime(seg.GetStart()),
		stop:        gst.ClockTime(seg.GetStop()),
		timeCT:      gst.ClockTime(seg.GetTime()),
		base:        gst.ClockTime(seg.GetBase()),
		position:    gst.ClockTime(seg.GetPosition()),
	}, true
}

// TraceNewSegmentFlow attaches probes to every element in 'root' (recursively).
// It logs the first origin of each NEWSEGMENT (by seqnum) and any element that
// modifies the segment between its sink→src.
func TraceNewSegmentFlow(root *Bin, logf func(msg string, kv ...any)) error {
	elems, err := root.bin.GetElementsRecursive()
	if err != nil {
		return err
	}

	var originOnce sync.Map // seqnum -> seen
	var incoming sync.Map   // key(elem,seq) -> segSnap
	key := func(e *gst.Element, seq uint32) string {
		return fmt.Sprintf("%p:%d", e.Instance(), seq)
	}

	for _, elem := range elems {
		// SINK pads: remember incoming segment (per seqnum)
		if sinkPads, _ := elem.GetSinkPads(); len(sinkPads) > 0 {
			for _, sp := range sinkPads {
				sp.AddProbe(gst.PadProbeTypeEventDownstream, func(_ *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
					ev := info.GetEvent()
					if ev == nil || ev.Type() != gst.EventTypeSegment {
						return gst.PadProbeOK
					}
					if snap, ok := snapFromEvent(ev); ok {
						incoming.Store(key(elem, ev.Seqnum()), snap)
					}
					return gst.PadProbeOK
				})
			}
		}

		// SRC pads: compare outgoing to incoming; log origin or modification
		if srcPads, _ := elem.GetSrcPads(); len(srcPads) > 0 {
			for _, sp := range srcPads {
				sp.AddProbe(gst.PadProbeTypeEventDownstream, func(p *gst.Pad, info *gst.PadProbeInfo) gst.PadProbeReturn {
					ev := info.GetEvent()
					if ev == nil || ev.Type() != gst.EventTypeSegment {
						return gst.PadProbeOK
					}
					seq := ev.Seqnum()
					out, ok := snapFromEvent(ev)
					if !ok {
						return gst.PadProbeOK
					}

					if v, found := incoming.LoadAndDelete(key(elem, seq)); found {
						in := v.(segSnap)
						if !in.equal(out) {
							logf("NEWSEGMENT modified",
								"elem", elem.GetName(), "pad", p.GetName(), "seqnum", seq,
								"in", in.String(), "out", out.String())
						}
					} else {
						// No incoming seen at this element: treat as origin (or we attached late).
						if _, seen := originOnce.LoadOrStore(seq, struct{}{}); !seen {
							logf("NEWSEGMENT origin",
								"elem", elem.GetName(), "pad", p.GetName(), "seqnum", seq,
								"snap", out.String())
						}
					}
					return gst.PadProbeOK
				})
			}
		}
	}
	return nil
}
