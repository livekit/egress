package source

import (
	"time"

	"github.com/livekit/protocol/auth"
	"github.com/livekit/protocol/utils"
)

func buildToken(apiKey, secret, roomName string) (string, error) {
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

	at := auth.NewAccessToken(apiKey, secret).
		AddGrant(grant).
		SetIdentity(utils.NewGuid(utils.EgressPrefix)).
		SetValidFor(24 * time.Hour)

	return at.ToJWT()
}
