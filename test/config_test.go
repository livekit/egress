package test

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
)

var defaultConfig = `
log_level: error
api_key: fake_key
api_secret: fake_secret
ws_url: wss://fake-url.com
room_only: false
track_composite_only: false
track_only: false
with_muting: false
gst_debug: 1
`

type testConfig struct {
	*config.Config

	RoomName           string `yaml:"room_name"`
	RoomOnly           bool   `yaml:"room_only"`
	TrackCompositeOnly bool   `yaml:"track_composite_only"`
	TrackOnly          bool   `yaml:"track_only"`
	WithMuting         bool   `yaml:"with_muting"`
	GstDebug           int    `yaml:"gst_debug"`
}

func getTestConfig(t *testing.T) *testConfig {
	confString := defaultConfig
	confFile := os.Getenv("EGRESS_CONFIG_FILE")
	if confFile != "" {
		b, err := ioutil.ReadFile(confFile)
		if err == nil {
			confString = string(b)
		}
	}

	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	tc := &testConfig{
		RoomName: "egress-test",
		GstDebug: 1,
	}
	err = yaml.Unmarshal([]byte(confString), tc)
	require.NoError(t, err)

	tc.Config = conf

	if tc.GstDebug != 0 {
		require.NoError(t, os.Setenv("GST_DEBUG", fmt.Sprint(tc.GstDebug)))
	}

	return tc
}
