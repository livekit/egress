package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

func TestConfig(t *testing.T) {
	req := &livekit.StartEgressRequest{
		EgressId: "egressID",
		Request: &livekit.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: "room",
				Layout:   "layout",
				Output: &livekit.RoomCompositeEgressRequest_File{
					File: &livekit.EncodedFileOutput{
						Filepath: "filepath",
						Output: &livekit.EncodedFileOutput_S3{
							S3: &livekit.S3Upload{
								AccessKey: "access",
								Secret:    "secret",
								Bucket:    "bucket",
							},
						},
					},
				},
			},
		},
		Token: "token",
		WsUrl: "wss://egress.com",
	}

	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			NodeID: "server",
		},
	}

	p, err := GetValidatedPipelineConfig(conf, req)
	require.NoError(t, err)

	require.Equal(t, "******", p.Info.GetRoomComposite().GetFile().GetS3().AccessKey)

	require.Len(t, p.Outputs, 1)
	output := p.Outputs[types.EgressTypeFile]
	require.NotNil(t, output.UploadConfig)
	require.Equal(t, "access", output.UploadConfig.(*livekit.S3Upload).AccessKey)
}
