package logging

import (
	"fmt"
	"strings"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
)

var sdkPrefixes = map[string]bool{
	"turnc": true, // turnc ERROR
	"ice E": true, // ice ERROR
	"pc ER": true, // pc ERROR
	"twcc_": true, // twcc_sender_interceptor ERROR
	"SDK 2": true, // SDK 2025
}

func NewHandlerLogger(handlerID, egressID string) *medialogutils.CmdLogger {
	l := logger.GetLogger().WithValues("handlerID", handlerID, "egressID", egressID)
	return medialogutils.NewCmdLogger(func(s string) {
		lines := strings.Split(s, "\n")
		for i, line := range lines {
			switch {
			case strings.HasSuffix(line, "}"):
				fmt.Println(line)

			case len(line) == 0:
				continue

			case len(line) > 5 && sdkPrefixes[line[:5]]:
				l.Infow(line)

			case strings.HasPrefix(line, "{\"level\":"):
				// should have ended with "}", probably got split
				var next string
				for j := i + 1; j < len(lines); j++ {
					next = lines[j]
					if len(next) > 0 && strings.HasSuffix(next, "}") {
						line += next
						break
					}
				}
				fmt.Println(line)

			case strings.HasPrefix(line, "(egress:"),
				strings.Contains(line, "before 'caps'"),
				strings.Contains(line, "' of type '"):
				logger.Warnw(line, nil)

			case strings.Contains(line, "unmarshal JSON string into Go network.CookiePartitionKey"),
				strings.HasPrefix(line, "0:00:"),
				strings.HasSuffix(line, "is not mapped"),
				strings.HasSuffix(line, "load cuda library"),
				strings.Contains(line, "libcuda.so.1"):
				continue

			default:
				l.Errorw(line, nil)
			}
		}
	})
}
