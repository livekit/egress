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
room_name: egress-test
room: true
track_composite: true
track: true
file: true
stream: true
muting: false
gst_debug: 1
`

type testConfig struct {
	*config.Config

	RoomName               string `yaml:"room_name"`
	RunRoomTests           bool   `yaml:"room"`
	RunTrackCompositeTests bool   `yaml:"track_composite"`
	RunTrackTests          bool   `yaml:"track"`
	RunFileTests           bool   `yaml:"file"`
	RunStreamTests         bool   `yaml:"stream"`
	Muting                 bool   `yaml:"muting"`
	GstDebug               int    `yaml:"gst_debug"`
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

	tc := &testConfig{}
	err = yaml.Unmarshal([]byte(confString), tc)
	require.NoError(t, err)

	tc.Config = conf

	if tc.GstDebug != 0 {
		require.NoError(t, os.Setenv("GST_DEBUG", fmt.Sprint(tc.GstDebug)))
	}

	return tc
}
