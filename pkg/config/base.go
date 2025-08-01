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

package config

import (
	"os"
	"strings"
	"time"

	"github.com/livekit/protocol/logger"
	"github.com/livekit/protocol/logger/medialogutils"
	"github.com/livekit/protocol/redis"
	lksdk "github.com/livekit/server-sdk-go/v2"
)

const TmpDir = "/home/egress/tmp"

type BaseConfig struct {
	NodeID string // do not supply - will be overwritten

	// required
	Redis     *redis.RedisConfig `yaml:"redis"`      // redis config
	ApiKey    string             `yaml:"api_key"`    // (env LIVEKIT_API_KEY)
	ApiSecret string             `yaml:"api_secret"` // (env LIVEKIT_API_SECRET)
	WsUrl     string             `yaml:"ws_url"`     // (env LIVEKIT_WS_URL)

	// optional
	Logging                      *logger.Config `yaml:"logging"`                          // logging config
	TemplateBase                 string         `yaml:"template_base"`                    // custom template base url
	ClusterID                    string         `yaml:"cluster_id"`                       // cluster this instance belongs to
	EnableChromeSandbox          bool           `yaml:"enable_chrome_sandbox"`            // enable Chrome sandbox, requires extra docker configuration
	MaxUploadQueue               int            `yaml:"max_upload_queue"`                 // maximum upload queue size, in minutes
	DisallowLocalStorage         bool           `yaml:"disallow_local_storage"`           // require an upload config for all requests
	EnableRoomCompositeSDKSource bool           `yaml:"enable_room_composite_sdk_source"` // attempt to render supported audio only room composite use cases using the SDK source instead of Chrome. This option will be removed when this becomes the default behavior eventually.
	IOCreateTimeout              time.Duration  `yaml:"io_create_timeout"`                // timeout for CreateEgress calls
	IOUpdateTimeout              time.Duration  `yaml:"io_update_timeout"`                // timeout for UpdateEgress calls

	SessionLimits      `yaml:"session_limits"` // session duration limits
	StorageConfig      *StorageConfig          `yaml:"storage,omitempty"`     // storage config
	BackupConfig       *StorageConfig          `yaml:"backup,omitempty"`      // backup config, for storage failures
	S3AssumeRoleKey    string                  `yaml:"s3_assume_role_key"`    // if set, this key is used for S3 uploads to assume the role defined in the assume_role_arn field of the S3 config
	S3AssumeRoleSecret string                  `yaml:"s3_assume_role_secret"` // if true, S3 uploads are allowed to use the S3 key and secret from StorageConfig to assume a role from an external account speicifed in the request assumed_role_arn field

	// advanced
	Insecure    bool                   `yaml:"insecure"`     // allow chrome to connect to an insecure websocket
	Debug       DebugConfig            `yaml:"debug"`        // create dot file on internal error
	ChromeFlags map[string]interface{} `yaml:"chrome_flags"` // additional flags to pass to Chrome
	Latency     LatencyConfig          `yaml:"latency"`      // gstreamer latencies, modifying these may break the service
}

type SessionLimits struct {
	FileOutputMaxDuration    time.Duration `yaml:"file_output_max_duration"`
	StreamOutputMaxDuration  time.Duration `yaml:"stream_output_max_duration"`
	SegmentOutputMaxDuration time.Duration `yaml:"segment_output_max_duration"`
	ImageOutputMaxDuration   time.Duration `yaml:"image_output_max_duration"`
}

type DebugConfig struct {
	EnableProfiling     bool             `yaml:"enable_profiling"`      // create dot file and pprof on internal error
	EnableTrackLogging  bool             `yaml:"enable_track_logging"`  // log packets and keyframes for each track
	EnableStreamLogging bool             `yaml:"enable_stream_logging"` // log bytes and keyframes for each stream
	EnableChromeLogging bool             `yaml:"enable_chrome_logging"` // log all chrome console events
	StorageConfig       `yaml:",inline"` // upload config (S3, Azure, GCP, or AliOSS)
}

type LatencyConfig struct {
	JitterBufferLatency time.Duration `yaml:"jitter_buffer_latency"` // jitter buffer max latency for sdk egress
	AudioMixerLatency   time.Duration `yaml:"audio_mixer_latency"`   // audio mixer latency, must be greater than jitter buffer latency
	PipelineLatency     time.Duration `yaml:"pipeline_latency"`      // max latency for the entire pipeline
}

func (c *BaseConfig) initLogger(values ...interface{}) error {
	_, exists := os.LookupEnv("GST_DEBUG")

	// If GST_DEBUG is not set, use pre-defined values based on logging level
	if !exists {
		var gstDebug []string
		switch c.Logging.Level {
		case "debug":
			gstDebug = []string{"3"}
		case "info", "warn":
			gstDebug = []string{"2"}
		case "error":
			gstDebug = []string{"1"}
		}
		gstDebug = append(gstDebug,
			"rtmpclient:4",
			"srtlib:1",
		)

		if err := os.Setenv("GST_DEBUG", strings.Join(gstDebug, ",")); err != nil {
			return err
		}
	}

	zl, err := logger.NewZapLogger(c.Logging)
	if err != nil {
		return err
	}

	l := zl.WithValues(values...)

	logger.SetLogger(l, "egress")
	lksdk.SetLogger(medialogutils.NewOverrideLogger(nil))
	return nil
}
