package sink

import (
	"net/http"
	"os"

	"github.com/livekit/protocol/livekit"
)

func UploadFile(filename string, url string, fileType livekit.EncodedFileType) error {
	f, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer func() {
		_ = f.Close()
	}()

	var contentType string
	switch fileType {
	case livekit.EncodedFileType_MP4:
		contentType = "video/mp4"
	case livekit.EncodedFileType_OGG:
		contentType = "audio/ogg"
	}

	resp, err := http.Post(url, contentType, f)
	if err != nil {
		return err
	}
	_ = resp.Body.Close()
	return nil
}
