//go:build integration

package test

import (
	"fmt"
	"io/ioutil"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/egress"
	"github.com/livekit/protocol/utils"
	lksdk "github.com/livekit/server-sdk-go"
)

type TestConfig struct {
	*config.Config

	// test config
	RoomName                string `yaml:"room_name"`
	RoomTestsOnly           bool   `yaml:"room_only"`
	ParticipantTestsOnly    bool   `yaml:"participant_only"`
	TrackCompositeTestsOnly bool   `yaml:"track_composite_only"`
	TrackTestsOnly          bool   `yaml:"track_only"`
	WebTestsOnly            bool   `yaml:"web_only"`
	FileTestsOnly           bool   `yaml:"file_only"`
	StreamTestsOnly         bool   `yaml:"stream_only"`
	SegmentTestsOnly        bool   `yaml:"segments_only"`
	Muting                  bool   `yaml:"muting"`
	GstDebug                int    `yaml:"gst_debug"`

	// test context
	svc       *service.Service `yaml:"-"`
	rpcClient egress.RPCClient `yaml:"-"`
	room      *lksdk.Room      `yaml:"-"`
	updates   utils.PubSub     `yaml:"-"`

	// helpers
	runRoomTests           bool `yaml:"-"`
	runParticipantTests    bool `yaml:"-"`
	runTrackCompositeTests bool `yaml:"-"`
	runTrackTests          bool `yaml:"-"`
	runWebTests            bool `yaml:"-"`
	runFileTests           bool `yaml:"-"`
	runStreamTests         bool `yaml:"-"`
	runSegmentTests        bool `yaml:"-"`
}

func NewTestContext(t *testing.T) *TestConfig {
	confString := os.Getenv("EGRESS_CONFIG_STRING")
	if confString == "" {
		confFile := os.Getenv("EGRESS_CONFIG_FILE")
		require.NotEmpty(t, confFile)
		b, err := ioutil.ReadFile(confFile)
		require.NoError(t, err)
		confString = string(b)
	}

	tc := &TestConfig{
		RoomName: "egress-test",
		Muting:   false,
		GstDebug: 1,
	}
	err := yaml.Unmarshal([]byte(confString), tc)
	require.NoError(t, err)

	conf, err := config.NewConfig(confString)
	require.NoError(t, err)
	tc.Config = conf

	if conf.ApiKey == "" || conf.ApiSecret == "" || conf.WsUrl == "" {
		t.Fatal("api key, secret, and ws url required")
	}
	if conf.Redis == nil {
		t.Fatal("redis required")
	}

	tc.runRoomTests = !tc.ParticipantTestsOnly && !tc.TrackCompositeTestsOnly && !tc.TrackTestsOnly && !tc.WebTestsOnly
	tc.runParticipantTests = !tc.RoomTestsOnly && !tc.TrackCompositeTestsOnly && !tc.TrackTestsOnly && !tc.WebTestsOnly
	tc.runTrackCompositeTests = !tc.RoomTestsOnly && !tc.ParticipantTestsOnly && !tc.TrackTestsOnly && !tc.WebTestsOnly
	tc.runTrackTests = !tc.RoomTestsOnly && !tc.ParticipantTestsOnly && !tc.TrackCompositeTestsOnly && !tc.WebTestsOnly
	tc.runWebTests = !tc.RoomTestsOnly && !tc.ParticipantTestsOnly && !tc.TrackCompositeTestsOnly && !tc.TrackTestsOnly
	tc.runFileTests = !tc.StreamTestsOnly && !tc.SegmentTestsOnly
	tc.runStreamTests = !tc.FileTestsOnly && !tc.SegmentTestsOnly
	tc.runSegmentTests = !tc.FileTestsOnly && !tc.StreamTestsOnly

	err = os.Setenv("GST_DEBUG", fmt.Sprint(tc.GstDebug))
	require.NoError(t, err)

	return tc
}
