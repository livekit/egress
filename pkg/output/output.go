package output

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/livekit/protocol/livekit"
	"github.com/tinyzimmer/go-gst/gst"
)

type Bin interface {
	LinkElements() error
	Bin() *gst.Bin
	AddSink(url string) error
	RemoveSink(url string) error
	RemoveSinkByName(name string) error
}

type Params struct {
	IsStream       bool
	StreamProtocol livekit.StreamProtocol
	StreamUrls     []string

	FileType livekit.EncodedFileType
	FileUrl  string
	RoomName string
}

func New(params *Params) (Bin, error) {
	if params.IsStream {
		return newStreamOutputBin(params.StreamProtocol, params.StreamUrls)
	} else {
		var filename string
		if strings.Contains(params.FileUrl, "://") {
			filename = fmt.Sprintf("%s-%v.%s",
				params.RoomName,
				time.Now().String(),
				strings.ToLower(params.FileType.String()),
			)
		} else {
			filename = params.FileUrl
			if idx := strings.LastIndex(filename, "/"); idx != -1 {
				if err := os.MkdirAll(filename[:idx], os.ModeDir); err != nil {
					return nil, err
				}
			}

			ext := "." + strings.ToLower(params.FileType.String())
			if !strings.HasSuffix(filename, ext) {
				filename = filename + ext
			}
		}
		return newFileOutputBin(filename)
	}
}
