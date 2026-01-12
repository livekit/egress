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

	"github.com/livekit/egress/pkg/types"
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
	IOSelectionTimeout           time.Duration  `yaml:"io_selection_timeout"`             // timeout for affinity stage of IO RPC
	IOWorkers                    int            `yaml:"io_workers"`                       // number of IO update workers

	SessionLimits          `yaml:"session_limits"` // session duration limits
	StorageConfig          *StorageConfig          `yaml:"storage,omitempty"`          // storage config
	BackupConfig           *StorageConfig          `yaml:"backup,omitempty"`           // backup config, for storage failures
	S3AssumeRoleKey        string                  `yaml:"s3_assume_role_key"`         // if set, this key is used for S3 uploads to assume the role defined in the assume_role_arn field of the S3 config
	S3AssumeRoleSecret     string                  `yaml:"s3_assume_role_secret"`      // if set, this secret is used for S3 uploads to assume the role defined in the assume_role_arn field of the S3 config
	S3AssumeRoleArn        string                  `yaml:"s3_assume_role_arn"`         // if set, this arn is used by default for S3 uploads
	S3AssumeRoleExternalID string                  `yaml:"s3_assume_role_external_id"` // if set, this external ID is used by default for S3 uploads

	// advanced
	Insecure             bool                                `yaml:"insecure"`               // allow chrome to connect to an insecure websocket
	Debug                DebugConfig                         `yaml:"debug"`                  // create dot file on internal error
	ChromeFlags          map[string]interface{}              `yaml:"chrome_flags"`           // additional flags to pass to Chrome
	Latency              LatencyConfig                       `yaml:"latency"`                // gstreamer latencies, modifying these may break the service
	LatencyOverrides     map[types.RequestType]LatencyConfig `yaml:"latency_overrides"`      // latency overrides for different request types, experimental only, will be removed
	AudioTempoController AudioTempoController                `yaml:"audio_tempo_controller"` // audio tempo controller
}

type SessionLimits struct {
	FileOutputMaxDuration    time.Duration `yaml:"file_output_max_duration"`
	FileOutputMaxSize        int64         `yaml:"file_output_max_size"` // max on-disk size in bytes before stopping; 0 to disable
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
	JitterBufferLatency               time.Duration `yaml:"jitter_buffer_latency"`                            // jitter buffer max latency for sdk egress
	AudioMixerLatency                 time.Duration `yaml:"audio_mixer_latency"`                              // audio mixer latency, must be greater than jitter buffer latency
	PipelineLatency                   time.Duration `yaml:"pipeline_latency"`                                 // max latency for the entire pipeline
	RTPMaxAllowedTsDiff               time.Duration `ymal:"rtp_max_allowed_ts_diff"`                          // max allowed PTS discont. for a RTP stream, before applying PTS alignment
	RTPMaxDriftAdjustment             time.Duration `ymal:"rtp_max_drift_adjustment,omitempty"`               // max allowed drift adjustment for a RTP stream
	RTPDriftAdjustmentWindowPercent   float64       `ymal:"rtp_drift_adjustment_window_percent,omitempty"`    // how much to throttle drift adjustment, 0.0 disables it
	PreJitterBufferReceiveTimeEnabled bool          `yaml:"pre_jitter_buffer_receive_time_enabled,omitempty"` // use packet arrival time in synchronizer
	OldPacketThreshold                time.Duration `yaml:"old_packet_threshold,omitempty"`                   // syncrhonizer drops packets older than this, 0 to disable packet drops
	RTCPSenderReportRebaseEnabled     bool          `yaml:"rtcp_sender_report_rebase_enabled,omitempty"`      // synchronizer will rebase RTCP Sender Report to local clock
	PacketBurstEstimatorEnabled       bool          `yaml:"packet_burst_estimator_enabled,omitempty"`         // enable burst estimator for improving track synchronization
	EnablePipelineTimeFeedback        bool          `yaml:"enable_pipeline_time_feedback,omitempty"`          // enable pipeline time feedback for synchronizer
}

type AudioTempoController struct {
	Enabled        bool    `yaml:"enabled"`         // enable audio tempo adjustments for compensating PTS drift
	AdjustmentRate float64 `yaml:"adjustment_rate"` // rate at which to adjust the tempo to compensate for PTS drift
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

func (c *BaseConfig) getLatencyConfig(requestType types.RequestType) LatencyConfig {
	if override, ok := c.LatencyOverrides[requestType]; ok {
		return override
	}
	return c.Latency
}
