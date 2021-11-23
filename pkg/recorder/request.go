package recorder

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/logger"
	livekit "github.com/livekit/protocol/proto"
	"github.com/livekit/protocol/utils"
)

var (
	ErrNoOutput        = errors.New("output file, s3 path, or rtmp urls required")
	ErrInvalidUrl      = errors.New("invalid rtmp url")
	ErrInvalidFilePath = errors.New("file output must be {path/}filename.mp4")
	ErrNoInput         = errors.New("input url or template required")
	ErrInvalidTemplate = errors.New("token or room name required")
)

func (r *Recorder) Validate(req *livekit.StartRecordingRequest) error {
	r.conf.ApplyDefaults(req)

	// validate input
	inputUrl, err := r.GetInputUrl(req)
	if err != nil {
		return err
	}

	// validate output
	switch req.Output.(type) {
	case *livekit.StartRecordingRequest_Rtmp:
		urls := req.Output.(*livekit.StartRecordingRequest_Rtmp).Rtmp.Urls
		if len(urls) == 0 {
			return ErrNoOutput
		}
		for _, u := range urls {
			if !strings.Contains(u, "://") {
				return ErrInvalidUrl
			}
		}
	case *livekit.StartRecordingRequest_Filepath:
		filepath := req.Output.(*livekit.StartRecordingRequest_Filepath).Filepath
		if !strings.HasSuffix(filepath, ".mp4") {
			return ErrInvalidFilePath
		}

		if r.conf.FileOutput.Local {
			// ensure directory exists
			if idx := strings.LastIndex(filepath, "/"); idx != -1 {
				if err = os.MkdirAll(filepath[:idx], os.ModeDir); err != nil {
					return err
				}
			}
			r.filename = filepath
		} else {
			if idx := strings.LastIndex(filepath, "/"); idx != -1 {
				// ignore directory for local write
				r.filename = filepath[idx+1:]
			} else {
				r.filename = filepath
			}
		}
		r.filepath = filepath
	default:
		return ErrNoOutput
	}

	r.req = req
	r.url = inputUrl
	logger.Debugw("request validated", "url", inputUrl)
	return nil
}

func (r *Recorder) GetInputUrl(req *livekit.StartRecordingRequest) (string, error) {
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
			return "", ErrInvalidTemplate
		}

		return fmt.Sprintf("%s/#/%s?url=%s&token=%s",
			r.conf.TemplateAddress, template.Layout, url.QueryEscape(r.conf.WsUrl), token), nil
	default:
		return "", ErrNoInput
	}
}

func (r *Recorder) buildToken(roomName string) (string, error) {
	f := false
	t := true
	grant := &auth.VideoGrant{
		RoomJoin:       true,
		Room:           roomName,
		CanSubscribe:   &t,
		CanPublish:     &f,
		CanPublishData: &f,
		Hidden:         true,
	}

	at := auth.NewAccessToken(r.conf.ApiKey, r.conf.ApiSecret).
		AddGrant(grant).
		SetIdentity(utils.NewGuid(utils.RecordingPrefix)).
		SetValidFor(24 * time.Hour)

	return at.ToJWT()
}
