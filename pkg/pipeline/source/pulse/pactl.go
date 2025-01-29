package pulse

import (
	"bytes"
	"encoding/json"
	"os/exec"

	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/logger"
)

func LogStatus() {
	info, err := List()
	if err != nil {
		logger.Warnw("failed to list pulse info", err)
	} else {
		b, _ := json.Marshal(info.GetEgressInfo())
		logger.Debugw("pulse status",
			"modules", len(info.Modules),
			"sinks", len(info.Sinks),
			"sources", len(info.Sources),
			"clients", len(info.Clients),
			"egresses", string(b),
		)
	}
}

func List() (*PulseInfo, error) {
	cmd := exec.Command("pactl", "--format", "json", "list")
	var b, e bytes.Buffer
	cmd.Stdout = &b
	cmd.Stderr = &e
	if cmd.Run() != nil {
		return nil, errors.New(e.String())
	}

	info := &PulseInfo{}
	return info, json.Unmarshal(b.Bytes(), info)
}

type PulseInfo struct {
	Modules       []Module       `json:"modules"`
	Sinks         []Device       `json:"sinks"`
	Sources       []Device       `json:"sources"`
	SinkInputs    []SinkInput    `json:"sink_inputs"`
	SourceOutputs []SourceOutput `json:"source_outputs"`
	Clients       []Client       `json:"clients"`
	Samples       []interface{}  `json:"samples"`
	Cards         []interface{}  `json:"cards"`
}

type Module struct {
	Name         string                 `json:"name"`
	Argument     string                 `json:"argument"`
	UsageCounter string                 `json:"usage_counter"`
	Properties   map[string]interface{} `json:"properties"`
}

type Device struct {
	Index               int                    `json:"index"`
	State               string                 `json:"state"`
	Name                string                 `json:"name"`
	Description         string                 `json:"description"`
	Driver              string                 `json:"driver"`
	SampleSpecification string                 `json:"sample_specification"`
	ChannelMap          string                 `json:"channel_map"`
	OwnerModule         int                    `json:"owner_module"`
	Mute                bool                   `json:"mute"`
	Volume              map[string]Volume      `json:"volume"`
	Balance             float64                `json:"balance"`
	BaseVolume          Volume                 `json:"base_volume"`
	MonitorSource       string                 `json:"monitor_source"`
	Latency             Latency                `json:"latency"`
	Flags               []string               `json:"flags"`
	Properties          map[string]interface{} `json:"properties"`
	Ports               []interface{}          `json:"ports"`
	ActivePort          interface{}            `json:"active_port"`
	Formats             []string               `json:"formats"`
}

type IOBase struct {
	Index               int                    `json:"index"`
	Driver              string                 `json:"driver"`
	OwnerModule         string                 `json:"owner_module"`
	Client              string                 `json:"client"`
	SampleSpecification string                 `json:"sample_specification"`
	ChannelMap          string                 `json:"channel_map"`
	Format              string                 `json:"format"`
	Corked              bool                   `json:"corked"`
	Mute                bool                   `json:"mute"`
	Volume              map[string]Volume      `json:"volume"`
	Balance             float64                `json:"balance"`
	BufferLatencyUSec   float64                `json:"buffer_latency_usec"`
	SinkLatencyUSec     float64                `json:"sink_latency_usec"`
	ResampleMethod      string                 `json:"resample_method"`
	Properties          map[string]interface{} `json:"properties"`
}

type SinkInput struct {
	IOBase `json:",inline"`
	Sink   int `json:"sink"`
}

type SourceOutput struct {
	IOBase `json:",inline"`
	Source int `json:"source"`
}

type Client struct {
	Index       int                    `json:"index"`
	Driver      string                 `json:"driver"`
	OwnerModule string                 `json:"owner_module"`
	Properties  map[string]interface{} `json:"properties"`
}

type Volume struct {
	Value        int    `json:"value"`
	ValuePercent string `json:"value_percent"`
	Db           string `json:"db"`
}

type Latency struct {
	Actual     float64 `json:"actual"`
	Configured float64 `json:"configured"`
}

type EgressInfo struct {
	EgressID      string
	SinkInputs    int
	SourceOutputs int
}

func (info *PulseInfo) GetEgressInfo() map[int]*EgressInfo {
	egressMap := make(map[int]*EgressInfo)
	for _, sink := range info.Sinks {
		egressMap[sink.Index] = &EgressInfo{
			EgressID: sink.Name,
		}
	}
	for _, sinkInput := range info.SinkInputs {
		egressMap[sinkInput.Sink].SinkInputs++
	}
	for _, sourceOutput := range info.SourceOutputs {
		egressMap[sourceOutput.Source].SourceOutputs++
	}
	return egressMap
}
