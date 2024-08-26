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

package uploader

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

const (
	getBucketLocationRegion = "us-east-1"
)

// S3Retryer wraps the SDK's built in DefaultRetryer adding additional
// custom features. Namely, to always retry.
type S3Retryer struct {
	client.DefaultRetryer
}

// ShouldRetry overrides the SDK's built in DefaultRetryer because the PUTs for segments/playlists are always idempotent
func (r S3Retryer) ShouldRetry(_ *request.Request) bool {
	return true
}

// S3Logger only logs aws messages on upload failure
type S3Logger struct {
	mu   sync.Mutex
	msgs []string
}

func (l *S3Logger) Log(msg string) {
	l.mu.Lock()
	l.msgs = append(l.msgs, msg)
	l.mu.Unlock()
}

func (l *S3Logger) Clear() {
	l.mu.Lock()
	l.msgs = nil
	l.mu.Unlock()
}

func (l *S3Logger) PrintLogs() {
	l.mu.Lock()
	for _, msg := range l.msgs {
		logger.Debugw(msg)
	}
	l.msgs = nil
	l.mu.Unlock()
}

type S3Uploader struct {
	awsConfig          *aws.Config
	logger             *S3Logger
	bucket             *string
	metadata           map[string]*string
	tagging            *string
	contentDisposition *string
}

func newS3Uploader(conf *config.EgressS3Upload) (uploader, error) {
	l := &S3Logger{}
	awsConfig := &aws.Config{
		Retryer: &S3Retryer{
			DefaultRetryer: client.DefaultRetryer{
				NumMaxRetries:    conf.MaxRetries,
				MaxRetryDelay:    conf.MaxRetryDelay,
				MaxThrottleDelay: conf.MaxRetryDelay,
				MinRetryDelay:    conf.MinRetryDelay,
				MinThrottleDelay: conf.MinRetryDelay,
			},
		},
		S3ForcePathStyle: aws.Bool(conf.ForcePathStyle),
		LogLevel:         &conf.AwsLogLevel,
		Logger: aws.LoggerFunc(func(args ...interface{}) {
			msg := "aws sdk:"
			for range len(args) {
				msg += " %v"
			}
			l.Log(fmt.Sprintf(msg, args...))
		}),
	}

	logger.Debugw("setting S3 config",
		"maxRetries", conf.MaxRetries,
		"maxDelay", conf.MaxRetryDelay,
		"minDelay", conf.MinRetryDelay,
	)
	if conf.AccessKey != "" && conf.Secret != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, conf.SessionToken)
	}
	if conf.Endpoint != "" {
		awsConfig.Endpoint = aws.String(conf.Endpoint)
	}
	if conf.Region != "" {
		awsConfig.Region = aws.String(conf.Region)
	}

	u := &S3Uploader{
		awsConfig: awsConfig,
		bucket:    aws.String(conf.Bucket),
	}

	if u.awsConfig.Region == nil {
		region, err := u.getBucketLocation()
		if err != nil {
			return nil, err
		}

		logger.Debugw("retrieved bucket location", "bucket", u.bucket, "location", region)

		u.awsConfig.Region = aws.String(region)
		u.logger.Clear()
	}

	if conf.Proxy != nil {
		proxyUrl, err := url.Parse(conf.Proxy.Url)
		if err != nil {
			return nil, err
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.Proxy = http.ProxyURL(proxyUrl)
		if conf.Proxy.Username != "" && conf.Proxy.Password != "" {
			auth := fmt.Sprintf("%s:%s", conf.Proxy.Username, conf.Proxy.Password)
			basicAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(auth))
			transport.ProxyConnectHeader = http.Header{}
			transport.ProxyConnectHeader.Add("Proxy-Authorization", basicAuth)
		}
		u.awsConfig.HTTPClient = &http.Client{Transport: transport}
	}

	if len(conf.Metadata) > 0 {
		u.metadata = make(map[string]*string, len(conf.Metadata))
		for k, v := range conf.Metadata {
			v := v
			u.metadata[k] = &v
		}
	}

	if conf.Tagging != "" {
		u.tagging = aws.String(conf.Tagging)
	}

	if conf.ContentDisposition != "" {
		u.contentDisposition = aws.String(conf.ContentDisposition)
	} else {
		// this is the default: https://developer.mozilla.org/en-US/docs/Web/HTTP/Headers/Content-Disposition#as_a_response_header_for_the_main_body
		u.contentDisposition = aws.String("inline")
	}

	return u, nil
}

func (u *S3Uploader) getBucketLocation() (string, error) {
	u.awsConfig.Region = aws.String(getBucketLocationRegion)

	sess, err := session.NewSession(u.awsConfig)
	if err != nil {
		return "", err
	}

	req := &s3.GetBucketLocationInput{
		Bucket: u.bucket,
	}

	svc := s3.New(sess)
	resp, err := svc.GetBucketLocation(req)
	if err != nil {
		return "", psrpc.NewErrorf(psrpc.InvalidArgument, "failed to retrieve upload bucket region: %v", err)
	}

	if resp.LocationConstraint == nil {
		return "", psrpc.NewErrorf(psrpc.InvalidArgument, "invalid upload bucket region returned by provider. Try specifying the region manually in the request")
	}

	return *resp.LocationConstraint, nil
}

func (u *S3Uploader) upload(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error) {
	sess, err := session.NewSession(u.awsConfig)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("S3", err)
	}

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, errors.ErrUploadFailed("S3", err)
	}
	defer func() {
		_ = file.Close()
	}()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, errors.ErrUploadFailed("S3", err)
	}

	_, err = s3manager.NewUploader(sess).Upload(&s3manager.UploadInput{
		Body:               file,
		Bucket:             u.bucket,
		ContentType:        aws.String(string(outputType)),
		Key:                aws.String(storageFilepath),
		Metadata:           u.metadata,
		Tagging:            u.tagging,
		ContentDisposition: u.contentDisposition,
	})
	if err != nil {
		u.logger.PrintLogs()
		return "", 0, errors.ErrUploadFailed("S3", err)
	} else {
		u.logger.Clear()
	}

	endpoint := "s3.amazonaws.com"
	if u.awsConfig.Endpoint != nil {
		endpoint = *u.awsConfig.Endpoint
	}

	return fmt.Sprintf("https://%s.%s/%s", *u.bucket, endpoint, storageFilepath), stat.Size(), nil
}
