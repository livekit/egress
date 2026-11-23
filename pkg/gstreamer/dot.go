// Copyright 2026 LiveKit, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package gstreamer

import "regexp"

// dotSecret matches the element properties that hold a stream key or a
// passphrase, up to the value: an RTMP location past its scheme, an SRT uri
// past its host, and the properties the sinks parse out of those.
//
// A graph lists an element's properties one per line, or after a literal \n,
// and again in a tooltip. A value ends at its closing \" or, cut short, at an
// ellipsis.
var dotSecret = regexp.MustCompile(
	`(?m)((?:^|\\n|&#10;|tooltip=")(?:` +
		`(?:stream|streamid|passphrase|username|password|secure-token)=\\"|` +
		`location=\\"rtmps?://|` +
		`uri=\\"srt://[^?"\\\n…]*\?` +
		`))(?:[^"\\\n…]|\\[^"n])*`)

// redactDot replaces the stream keys and passphrases in a pipeline graph.
func redactDot(dot string) string {
	return dotSecret.ReplaceAllString(dot, "${1}{redacted}")
}
