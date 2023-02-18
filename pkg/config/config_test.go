package config

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/rpc"
)

func TestRedactUpload(t *testing.T) {
	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			NodeID: "server",
		},
	}

	fileReq := &rpc.StartEgressRequest{
		EgressId: "egressID",
		Request: &rpc.StartEgressRequest_RoomComposite{
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

	p, err := GetValidatedPipelineConfig(conf, fileReq)
	require.NoError(t, err)

	require.Equal(t, "******", p.Info.GetRoomComposite().GetFile().GetS3().AccessKey)

	require.Len(t, p.Outputs, 1)
	output := p.Outputs[types.EgressTypeFile]
	require.NotNil(t, output.UploadConfig)
	require.Equal(t, "access", output.UploadConfig.(*livekit.S3Upload).AccessKey)
}

func TestRedactStreamKeys(t *testing.T) {
	var (
		streamUrl1   = "rtmp://sfo.contribute.live-video.net/app/stream_key"
		redactedUrl1 = "rtmp://sfo.contribute.live-video.net/app/**********"
		streamUrl2   = "rtmps://live-api-s.facebook.com:443/rtmp/stream_key"
		redactedUrl2 = "rtmps://live-api-s.facebook.com:443/rtmp/**********"
	)

	conf := &ServiceConfig{
		BaseConfig: BaseConfig{
			NodeID: "server",
		},
	}

	streamReq := &rpc.StartEgressRequest{
		EgressId: "egressID",
		Request: &rpc.StartEgressRequest_RoomComposite{
			RoomComposite: &livekit.RoomCompositeEgressRequest{
				RoomName: "room",
				Layout:   "layout",
				Output: &livekit.RoomCompositeEgressRequest_Stream{
					Stream: &livekit.StreamOutput{
						Urls: []string{
							streamUrl1,
							streamUrl2,
						},
					},
				},
			},
		},
		Token: "token",
		WsUrl: "wss://egress.com",
	}

	p, err := GetValidatedPipelineConfig(conf, streamReq)
	require.NoError(t, err)

	urls := p.Info.GetRoomComposite().GetStream().GetUrls()
	require.Len(t, urls, 2)
	require.Equal(t, redactedUrl1, urls[0])
	require.Equal(t, redactedUrl2, urls[1])

	streamInfo := p.Info.GetStream()
	require.Len(t, streamInfo.Info, 2)
	require.Equal(t, redactedUrl1, streamInfo.Info[0].Url)
	require.Equal(t, redactedUrl2, streamInfo.Info[1].Url)

	require.Len(t, p.Outputs, 1)
	output := p.Outputs[types.EgressTypeStream]
	require.Len(t, output.StreamUrls, 2)
	require.Equal(t, streamUrl1, output.StreamUrls[0])
	require.Equal(t, streamUrl2, output.StreamUrls[1])
}
