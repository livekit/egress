package builder

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"time"

	"github.com/go-gst/go-glib/glib"
	"github.com/go-gst/go-gst/gst"
	"github.com/livekit/protocol/logger"
)

const (
	OpusDecStatsStructName       = "livekit-opus-plc-stats"
	OpusDecStatsKeyPlcDurationNs = "plc-duration-ns"
	OpusDecStatsKeyPlcNumSamples = "plc-num-samples"
	OpusDecStatsKeyNumGap        = "num-gap"
	OpusDecStatsKeyNumPushed     = "num-pushed"
)

var (
	reU64 = map[string]*regexp.Regexp{
		"num-pushed":      regexp.MustCompile(`\bnum-pushed=\(g?uint64\)(\d+)`),
		"num-gap":         regexp.MustCompile(`\bnum-gap=\(g?uint64\)(\d+)`),
		"plc-num-samples": regexp.MustCompile(`\bplc-num-samples=\(g?uint64\)(\d+)`),
		"plc-duration":    regexp.MustCompile(`\bplc-duration=\(g?uint64\)(\d+)`), // ns
	}
	reU32 = map[string]*regexp.Regexp{
		"sample-rate": regexp.MustCompile(`\bsample-rate=\(uint\)(\d+)`),
		"channels":    regexp.MustCompile(`\bchannels=\(uint\)(\d+)`),
	}
)

type OpusDecStats struct {
	NumPushed     uint64
	NumGap        uint64
	PlcNumSamples uint64
	PlcDuration   time.Duration // ns
	SampleRate    uint32
	Channels      uint32
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

	getU64 := func(k string) (uint64, error) {
		if m := reU64[k].FindStringSubmatch(s); len(m) == 2 {
			return strconv.ParseUint(m[1], 10, 64)
		}
		return 0, fmt.Errorf("missing %s", k)
	}
	getU32 := func(k string) (uint32, error) {
		if m := reU32[k].FindStringSubmatch(s); len(m) == 2 {
			v, _ := strconv.ParseUint(m[1], 10, 32)
			return uint32(v), nil
		}
		return 0, fmt.Errorf("missing %s", k)
	}

	// Required
	if v, err := getU64("num-pushed"); err == nil {
		st.NumPushed = v
	}
	if v, err := getU64("num-gap"); err == nil {
		st.NumGap = v
	}
	if v, err := getU64("plc-num-samples"); err == nil {
		st.PlcNumSamples = v
	}
	if v, err := getU64("plc-duration"); err == nil {
		st.PlcDuration = time.Duration(v) * time.Nanosecond
	}

	// Optional
	if v, err := getU32("sample-rate"); err == nil {
		st.SampleRate = v
	}
	if v, err := getU32("channels"); err == nil {
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
