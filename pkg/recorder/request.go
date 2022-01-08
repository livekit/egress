package recorder

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/utils"
)

var (
	ErrNoOutput        = errors.New("output file, s3 path, or rtmp urls required")
	ErrInvalidUrl      = errors.New("invalid rtmp url")
	ErrInvalidFilePath = errors.New("file output must be {path/}filename.mp4")
	ErrNoInput         = errors.New("input url or template required")
)

func (r *Recorder) Validate(req *livekit.StartRecordingRequest) error {
	r.conf.ApplyDefaults(req)

	// validate input
	inputUrl, isTemplate, err := r.GetInputUrl(req)
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
	r.isTemplate = isTemplate
	r.url = inputUrl
	logger.Debugw("request validated", "url", inputUrl)
	return nil
}

func (r *Recorder) GetInputUrl(req *livekit.StartRecordingRequest) (string, bool, error) {
	switch req.Input.(type) {
	case *livekit.StartRecordingRequest_Url:
		return req.Input.(*livekit.StartRecordingRequest_Url).Url, false, nil
	case *livekit.StartRecordingRequest_Template:
		template := req.Input.(*livekit.StartRecordingRequest_Template).Template
		if template.RoomName == "" {
			return "", true, errors.New("room name required for template input")
		}

		r.result.RoomName = template.RoomName
		token, err := r.buildToken(template.RoomName)
		if err != nil {
			return "", true, err
		}

		baseUrl := r.conf.TemplateAddress
		if template.BaseUrl != "" {
			baseUrl = template.BaseUrl
		}

		return fmt.Sprintf("%s/%s?url=%s&token=%s",
			baseUrl, template.Layout, url.QueryEscape(r.conf.WsUrl), token), true, nil
	default:
		return "", false, ErrNoInput
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
		Recorder:       true,
	}

	at := auth.NewAccessToken(r.conf.ApiKey, r.conf.ApiSecret).
		AddGrant(grant).
		SetIdentity(utils.NewGuid(utils.RecordingPrefix)).
		SetValidFor(24 * time.Hour)

	return at.ToJWT()
}
