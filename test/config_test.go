//go:build integration

package test

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"

	"github.com/livekit/egress/pkg/config"
)

type testConfig struct {
	*config.Config

	RoomName                string `yaml:"room_name"`
	RoomTestsOnly           bool   `yaml:"room_only"`
	TrackCompositeTestsOnly bool   `yaml:"track_composite_only"`
	TrackTestsOnly          bool   `yaml:"track_only"`
	FileTestsOnly           bool   `yaml:"file_only"`
	StreamTestsOnly         bool   `yaml:"stream_only"`
	SegmentedFileTestsOnly  bool   `yaml:"segments_only"`
	Muting                  bool   `yaml:"muting"`
	GstDebug                int    `yaml:"gst_debug"`

	svc       *service.Service `yaml:"-"`
	rpcClient egress.RPCClient `yaml:"-"`
	room      *lksdk.Room      `yaml:"-"`
	updates   utils.PubSub     `yaml:"-"`
}

func getTestConfig(t *testing.T) *testConfig {
	confFile := os.Getenv("EGRESS_CONFIG_FILE")
	require.NotEmpty(t, confFile)
	b, err := ioutil.ReadFile(confFile)
	require.NoError(t, err)

	tc := &testConfig{
		RoomName: "egress-test",
		Muting:   false,
		GstDebug: 1,
	}
	err = yaml.Unmarshal(b, tc)
	require.NoError(t, err)

	conf, err := config.NewConfig(string(b))
	require.NoError(t, err)
	tc.Config = conf

	if conf.ApiKey == "" || conf.ApiSecret == "" || conf.WsUrl == "" {
		t.Fatal("api key, secret, and ws url required")
	}
	if conf.Redis == nil {
		t.Fatal("redis required")
	}

	err = os.Setenv("GST_DEBUG", fmt.Sprint(tc.GstDebug))
	require.NoError(t, err)

	return tc
}
