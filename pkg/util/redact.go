package util

import (
	"regexp"
	"strings"
)

// rtmp urls must be of format rtmp(s)://{host}(/{path})/{app}/{stream_key}( live=1)
var rtmpRegexp = regexp.MustCompile("^(rtmps?:\\/\\/)(.*\\/)(.*\\/)(\\S*)( live=1)?$")

func RedactStreamKey(url string) (string, bool) {
	match := rtmpRegexp.FindStringSubmatch(url)
	if len(match) != 6 {
		return url, false
	}

	match[4] = Redact(match[4])
	return strings.Join(match[1:], ""), true
}

func Redact(s string) string {
	return strings.Repeat("*", len(s))
}
