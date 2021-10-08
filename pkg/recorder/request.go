package recorder

import (
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/livekit/protocol/auth"
	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/utils"
)

func (r *Recorder) getInputUrl(req *livekit.StartRecordingRequest) (string, error) {
	switch req.Input.(type) {
	case *livekit.StartRecordingRequest_Url:
		return req.Input.(*livekit.StartRecordingRequest_Url).Url, nil
	case *livekit.StartRecordingRequest_Template:
		template := req.Input.(*livekit.StartRecordingRequest_Template).Template

		var token string
		switch template.Room.(type) {
		case *livekit.RecordingTemplate_RoomName:
			var err error
			token, err = r.buildToken(template.Room.(*livekit.RecordingTemplate_RoomName).RoomName)
			if err != nil {
				return "", err
			}
		case *livekit.RecordingTemplate_Token:
			token = template.Room.(*livekit.RecordingTemplate_Token).Token
		default:
			return "", errors.New("token or room name required")
		}

		return fmt.Sprintf("https://recorder.livekit.io/#/%s?url=%s&token=%s",
			template.Layout, url.QueryEscape(r.conf.WsUrl), token), nil
	default:
		return "", errors.New("input url or template required")
	}
}

func (r *Recorder) buildToken(roomName string) (string, error) {
	f := false
	t := true
	grant := &auth.VideoGrant{
		RoomRecord:   true,
		Room:         roomName,
		CanPublish:   &f,
		CanSubscribe: &t,
		Hidden:       true,
	}

	at := auth.NewAccessToken(r.conf.ApiKey, r.conf.ApiSecret).
		AddGrant(grant).
		SetIdentity(utils.NewGuid(utils.RecordingPrefix)).
		SetValidFor(24 * time.Hour)

	return at.ToJWT()
}
