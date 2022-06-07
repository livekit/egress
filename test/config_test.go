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

type testConfig struct {
	*config.Config

	RoomName               string `yaml:"room_name"`
	RunServiceTest         bool   `yaml:"service"`
	RunRoomTests           bool   `yaml:"room"`
	RunTrackCompositeTests bool   `yaml:"track_composite"`
	RunTrackTests          bool   `yaml:"track"`
	RunFileTests           bool   `yaml:"file"`
	RunStreamTests         bool   `yaml:"stream"`
	RunSegmentedFileTests  bool   `yaml:"segments"`
	Muting                 bool   `yaml:"muting"`
	GstDebug               int    `yaml:"gst_debug"`

	HasConnectionInfo bool `yaml:"-"`
	HasRedis          bool `yaml:"-"`
}

func getTestConfig(t *testing.T) *testConfig {
	var confString string
	confFile := os.Getenv("EGRESS_CONFIG_FILE")
	if confFile != "" {
		b, err := ioutil.ReadFile(confFile)
		if err == nil {
			confString = string(b)
		}
	}

	tc := &testConfig{
		RoomName:               "egress-test",
		RunRoomTests:           true,
		RunTrackCompositeTests: false,
		RunTrackTests:          false,
		RunFileTests:           true,
		RunStreamTests:         true,
		RunSegmentedFileTests:  false,
		Muting:                 false,
		GstDebug:               1,
	}
	err := yaml.Unmarshal([]byte(confString), tc)
	require.NoError(t, err)

	conf, err := config.NewConfig(confString)
	require.NoError(t, err)

	tc.Config = conf
	if conf.ApiKey != "" && conf.ApiSecret != "" && conf.WsUrl != "" {
		tc.HasConnectionInfo = true
	} else {
		if conf.ApiKey == "" {
			conf.ApiKey = "fake_key"
		}
		if conf.ApiSecret == "" {
			conf.ApiSecret = "fake_secret"
		}
		if conf.WsUrl == "" {
			conf.WsUrl = "wss://fake-url.com"
		}
	}

	if conf.Redis.Address != "" {
		tc.HasRedis = true
	}

	require.NoError(t, os.Setenv("GST_DEBUG", fmt.Sprint(tc.GstDebug)))
	return tc
}
