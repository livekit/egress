package builder

import (
	"fmt"
	"log"
	"regexp"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

const (
	OpusDecStatsStructName       = "livekit-opus-plc-stats"
	OpusDecStatsKeyPlcDurationNs = "plc-duration-ns"
	OpusDecStatsKeyPlcNumSamples = "plc-num-samples"
	OpusDecStatsKeyNumGap        = "num-gap"
	OpusDecStatsKeyNumPushed     = "num-pushed"
)

var (
	rePushed        = regexp.MustCompile(`\bnum-pushed=\(g?uint64\)(\d+)`)
	reGap           = regexp.MustCompile(`\bnum-gap=\(g?uint64\)(\d+)`)
	rePlcNumSamples = regexp.MustCompile(`\bplc-num-samples=\(g?uint64\)(\d+)`)
	rePlcDuration   = regexp.MustCompile(`\bplc-duration=\(g?uint64\)(\d+)`)
	reSampleRate    = regexp.MustCompile(`\bsample-rate=\(uint\)(\d+)`)
	reChannels      = regexp.MustCompile(`\bchannels=\(uint\)(\d+)`)
)

type OpusDecStats struct {
	NumPushed     uint64
	NumGap        uint64
	PlcNumSamples uint64
	PlcDuration   time.Duration // ns
	SampleRate    uint64
	Channels      uint64
}

func serializeOpusStats(opusdec *gst.Element) (string, bool) {
	gvAny, err := opusdec.GetProperty("stats")
	if err != nil {
		log.Printf("opusdec stats: get failed: %v", err)
		return "", false
	}
	switch v := gvAny.(type) {
	case *gst.Structure:
		return v.String(), true
	case *glib.Value:
		return gst.ValueSerialize(v), true
	case string:
		return v, true
	default:
		log.Printf("opusdec stats: unexpected type %T", gvAny)
		return "", false
	}
}

/*** minimal parser for the serialized GstStructure ***/

func parseStatsString(s string) (OpusDecStats, error) {
	var st OpusDecStats

	kv := utils.NewKVRegexScanner(s)

	if v, ok := kv.Uint64(rePushed); ok {
		st.NumPushed = v
	}
	if v, ok := kv.Uint64(reGap); ok {
		st.NumGap = v
	}
	if v, ok := kv.Uint64(rePlcNumSamples); ok {
		st.PlcNumSamples = v
	}
	if v, ok := kv.DurationNs(rePlcDuration); ok {
		st.PlcDuration = v
	}
	if v, ok := kv.Uint64(reSampleRate); ok {
		st.SampleRate = v
	}
	if v, ok := kv.Uint64(reChannels); ok {
		st.Channels = v
	}
	return st, nil
}

func getOpusDecStats(opusdec *gst.Element) (OpusDecStats, error) {
	ser, ok := serializeOpusStats(opusdec)
	if !ok {
		return OpusDecStats{}, fmt.Errorf("serialize error")
	}
	return parseStatsString(ser)
}

func postOpusDecStatsMessage(src *gst.Element, stats OpusDecStats) {
	s := gst.NewStructureFromString(
		fmt.Sprintf("%s, %s=(guint64)%d, %s=(guint64)%d, %s=(guint64)%d, %s=(guint64)%d",
			OpusDecStatsStructName,
			OpusDecStatsKeyPlcDurationNs, stats.PlcDuration.Nanoseconds(),
			OpusDecStatsKeyPlcNumSamples, stats.PlcNumSamples,
			OpusDecStatsKeyNumGap, stats.NumGap,
			OpusDecStatsKeyNumPushed, stats.NumPushed,
		))
	msg := gst.NewElementMessage(src, s)
	sent := src.PostMessage(msg)
	if !sent {
		logger.Debugw("failed to send opusdec PLC stats")
	}
}
