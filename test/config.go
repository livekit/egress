//go:build integration

package test

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/service"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/rpc"
	lksdk "github.com/livekit/server-sdk-go"
)

type TestConfig struct {
	*config.ServiceConfig

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
	MultiTestsOnly          bool   `yaml:"multi_only"`
	Muting                  bool   `yaml:"muting"`
	GstDebug                string `yaml:"gst_debug"`
	Short                   bool   `yaml:"short"`

	// test context
	svc         *service.Service         `yaml:"-"`
	client      rpc.EgressClient         `yaml:"-"`
	room        *lksdk.Room              `yaml:"-"`
	updates     chan *livekit.EgressInfo `yaml:"-"`
	S3Upload    *livekit.S3Upload        `yaml:"-"`
	GCPUpload   *livekit.GCPUpload       `yaml:"-"`
	AzureUpload *livekit.AzureBlobUpload `yaml:"-"`

	// helpers
	runRoomTests           bool `yaml:"-"`
	runParticipantTests    bool `yaml:"-"`
	runTrackCompositeTests bool `yaml:"-"`
	runTrackTests          bool `yaml:"-"`
	runWebTests            bool `yaml:"-"`
	runFileTests           bool `yaml:"-"`
	runStreamTests         bool `yaml:"-"`
	runSegmentTests        bool `yaml:"-"`
	runMultiTests          bool `yaml:"-"`

	sourceFramerate float64 `yaml:"-"`
}

func NewTestContext(t *testing.T) *TestConfig {
	confString := os.Getenv("EGRESS_CONFIG_STRING")
	if confString == "" {
		confFile := os.Getenv("EGRESS_CONFIG_FILE")
		require.NotEmpty(t, confFile)
		b, err := os.ReadFile(confFile)
		require.NoError(t, err)
		confString = string(b)
	}

	tc := &TestConfig{
		RoomName: "egress-test",
		Muting:   false,
		GstDebug: "1",
	}
	err := yaml.Unmarshal([]byte(confString), tc)
	require.NoError(t, err)

	conf, err := config.NewServiceConfig(confString)
	require.NoError(t, err)
	tc.ServiceConfig = conf

	if conf.ApiKey == "" || conf.ApiSecret == "" || conf.WsUrl == "" {
		t.Fatal("api key, secret, and ws url required")
	}
	if conf.Redis == nil {
		t.Fatal("redis required")
	}

	if s3 := os.Getenv("S3_UPLOAD"); s3 != "" {
		logger.Infow("using s3 uploads")
		tc.S3Upload = &livekit.S3Upload{}
		require.NoError(t, json.Unmarshal([]byte(s3), tc.S3Upload))
	} else {
		logger.Infow("no s3 config supplied")
	}

	if gcp := os.Getenv("GCP_UPLOAD"); gcp != "" {
		logger.Infow("using gcp uploads")
		tc.GCPUpload = &livekit.GCPUpload{}
		require.NoError(t, json.Unmarshal([]byte(gcp), tc.GCPUpload))
	} else {
		logger.Infow("no gcp config supplied")
	}

	if azure := os.Getenv("AZURE_UPLOAD"); azure != "" {
		logger.Infow("using azure uploads")
		tc.AzureUpload = &livekit.AzureBlobUpload{}
		require.NoError(t, json.Unmarshal([]byte(azure), tc.AzureUpload))
	} else {
		logger.Infow("no azure config supplied")
	}

	tc.runRoomTests = !tc.ParticipantTestsOnly && !tc.TrackCompositeTestsOnly && !tc.TrackTestsOnly && !tc.WebTestsOnly
	tc.runParticipantTests = !tc.RoomTestsOnly && !tc.TrackCompositeTestsOnly && !tc.TrackTestsOnly && !tc.WebTestsOnly
	tc.runTrackCompositeTests = !tc.RoomTestsOnly && !tc.ParticipantTestsOnly && !tc.TrackTestsOnly && !tc.WebTestsOnly
	tc.runTrackTests = !tc.RoomTestsOnly && !tc.ParticipantTestsOnly && !tc.TrackCompositeTestsOnly && !tc.WebTestsOnly
	tc.runWebTests = !tc.RoomTestsOnly && !tc.ParticipantTestsOnly && !tc.TrackCompositeTestsOnly && !tc.TrackTestsOnly
	tc.runFileTests = !tc.StreamTestsOnly && !tc.SegmentTestsOnly && !tc.MultiTestsOnly
	tc.runStreamTests = !tc.FileTestsOnly && !tc.SegmentTestsOnly && !tc.MultiTestsOnly
	tc.runSegmentTests = !tc.FileTestsOnly && !tc.StreamTestsOnly && !tc.MultiTestsOnly
	tc.runMultiTests = !tc.FileTestsOnly && !tc.StreamTestsOnly && !tc.SegmentTestsOnly

	err = os.Setenv("GST_DEBUG", tc.GstDebug)
	require.NoError(t, err)

	return tc
}
