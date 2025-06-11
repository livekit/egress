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

var gstSuffixes = map[string]bool{
	"before 'caps'": true,
	"f type 'gint'": true,
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
				if i < len(lines)-1 && strings.HasSuffix(lines[i+1], "}") {
					line = line + lines[i+1]
					i++
				}
				fmt.Println(line)
			case strings.HasPrefix(line, "(egress:"):
				if len(line) > 13 && !gstSuffixes[line[len(line)-13:]] && i < len(lines)-1 {
					next := lines[i+1]
					if len(next) > 13 && gstSuffixes[next[:13]] {
						// line got split
						line = line + next
						i++
					}
				}
				logger.Warnw(line, nil)
			case strings.HasPrefix(line, "0:00:"):
				if !strings.HasSuffix(line, "is not mapped") {
					// line got split
					if i < len(lines)-1 && strings.HasSuffix(lines[i+1], "is not mapped") {
						i++
					}
				}
				continue
			case strings.Contains(s, "unmarshal JSON string into Go network.CookiePartitionKey"):
				continue
			default:
				l.Errorw(line, nil)
			}
		}
	})
}
