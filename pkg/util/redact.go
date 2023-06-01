package util

import (
	"fmt"
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

	match[4] = redactStreamKey(match[4])
	return strings.Join(match[1:], ""), true
}

func redactStreamKey(key string) string {
	var prefix, suffix string
	for i := 3; i > 0; i-- {
		if len(key) >= i*3 {
			prefix = key[:i]
			suffix = key[len(key)-i:]
			break
		}
	}

	return fmt.Sprintf("{%s...%s}", prefix, suffix)
}

func Redact(s, name string) string {
	if s != "" {
		return name
	}
	return ""
}
