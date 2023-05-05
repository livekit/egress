package sink

import (
	"encoding/json"
	"os"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/pipeline/sink/uploader"
	"github.com/livekit/egress/pkg/types"
)

type Manifest struct {
	EgressID          string `json:"egress_id,omitempty"`
	RoomID            string `json:"room_id,omitempty"`
	RoomName          string `json:"room_name,omitempty"`
	Url               string `json:"url,omitempty"`
	StartedAt         int64  `json:"started_at,omitempty"`
	EndedAt           int64  `json:"ended_at,omitempty"`
	PublisherIdentity string `json:"publisher_identity,omitempty"`
	TrackID           string `json:"track_id,omitempty"`
	TrackKind         string `json:"track_kind,omitempty"`
	TrackSource       string `json:"track_source,omitempty"`
	AudioTrackID      string `json:"audio_track_id,omitempty"`
	VideoTrackID      string `json:"video_track_id,omitempty"`
	SegmentCount      int64  `json:"segment_count,omitempty"`
}

func uploadManifest(p *config.PipelineConfig, u *uploader.Uploader, localFilepath, storageFilepath string) error {
	manifest, err := os.Create(localFilepath)
	if err != nil {
		return err
	}

	b, err := getManifest(p)
	if err != nil {
		return err
	}

	_, err = manifest.Write(b)
	if err != nil {
		return err
	}

	_, _, err = u.Upload(localFilepath, storageFilepath, types.OutputTypeJSON)
	return err
}

func getManifest(p *config.PipelineConfig) ([]byte, error) {
	manifest := Manifest{
		EgressID:          p.Info.EgressId,
		RoomID:            p.Info.RoomId,
		RoomName:          p.Info.RoomName,
		Url:               p.WebUrl,
		StartedAt:         p.Info.StartedAt,
		EndedAt:           p.Info.EndedAt,
		PublisherIdentity: p.ParticipantIdentity,
		TrackID:           p.TrackID,
		TrackKind:         p.TrackKind,
		TrackSource:       p.TrackSource,
		AudioTrackID:      p.AudioTrackID,
		VideoTrackID:      p.VideoTrackID,
	}

	if o := p.GetSegmentConfig(); o != nil {
		manifest.SegmentCount = o.SegmentsInfo.SegmentCount
	}

	return json.Marshal(manifest)
}
