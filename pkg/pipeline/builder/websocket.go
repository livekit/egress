// Copyright 2023 LiveKit, Inc.
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

package builder

import (
	"github.com/go-gst/go-gst/gst"
	"github.com/go-gst/go-gst/gst/app"

	"github.com/livekit/egress/pkg/gstreamer"
)

func BuildWebsocketBin(pipeline *gstreamer.Pipeline, appSinkCallbacks *app.SinkCallbacks) (*gstreamer.Bin, error) {
	b := pipeline.NewBin("websocket")

	appSink, err := app.NewAppSink()
	if err != nil {
		return nil, err
	}
	appSink.SetCallbacks(appSinkCallbacks)

	if err = b.AddElement(appSink.Element); err != nil {
		return nil, err
	}

	b.SetGetSrcPad(func(name string) *gst.Pad {
		return appSink.GetStaticPad("sink")
	})

	return b, nil
}
