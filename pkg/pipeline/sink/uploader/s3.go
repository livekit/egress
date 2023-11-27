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
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/client"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/request"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/aws/aws-sdk-go/service/s3/s3manager"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/logger"
	"github.com/livekit/psrpc"
)

const (
	getBucketLocationRegion = "us-east-1"
)

// CustomRetryer wraps the SDK's built in DefaultRetryer adding additional
// custom features. Namely, to always retry.
type CustomRetryer struct {
	client.DefaultRetryer
}

// ShouldRetry overrides the SDK's built in DefaultRetryer because the PUTs for segments/playlists are always idempotent
func (r CustomRetryer) ShouldRetry(req *request.Request) bool {
	// if the request failed due to a 401 or 403, don't retry
	if req.HTTPResponse.StatusCode == http.StatusUnauthorized || req.HTTPResponse.StatusCode == http.StatusForbidden {
		return false
	}
	return true
}

type S3Uploader struct {
	awsConfig          *aws.Config
	bucket             *string
	metadata           map[string]*string
	tagging            *string
	contentDisposition *string
}

func newS3Uploader(conf *config.EgressS3Upload) (uploader, error) {
	awsConfig := &aws.Config{
		Retryer: &CustomRetryer{
			DefaultRetryer: client.DefaultRetryer{
				NumMaxRetries:    conf.MaxRetries,
				MaxRetryDelay:    conf.MaxRetryDelay,
				MaxThrottleDelay: conf.MaxRetryDelay,
				MinRetryDelay:    conf.MinRetryDelay,
				MinThrottleDelay: conf.MinRetryDelay,
			},
		},
		S3ForcePathStyle: aws.Bool(conf.ForcePathStyle),
		LogLevel:         aws.LogLevel(conf.AwsLogLevel),
	}
	logger.Debugw("setting S3 config",
		"maxRetries", conf.MaxRetries,
		"maxDelay", conf.MaxRetryDelay,
		"minDelay", conf.MinRetryDelay,
	)
	if conf.AccessKey != "" && conf.Secret != "" {
		awsConfig.Credentials = credentials.NewStaticCredentials(conf.AccessKey, conf.Secret, "")
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
	}

	if conf.Proxy != "" {
		logger.Debugw("configuring s3 with proxy", "proxyEndpoint", conf.Proxy)
		// Proxy configuration
		proxyURL, err := url.Parse(conf.Proxy)
		if err != nil {
			logger.Errorw("failed to parse proxy URL -- proxy not set", err, "proxy", conf.Proxy)
		} else {
			proxyTransport := &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			}
			u.awsConfig.HTTPClient = &http.Client{Transport: proxyTransport}
		}
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
		return "", psrpc.NewErrorf(psrpc.Unknown, "failed to retrieve upload bucket region: %v", err)
	}

	if resp.LocationConstraint == nil {
		return "", psrpc.NewErrorf(psrpc.MalformedResponse, "invalid upload bucket region returned by provider. Try specifying the region manually in the request")
	}

	return *resp.LocationConstraint, nil
}

func (u *S3Uploader) upload(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error) {
	sess, err := session.NewSession(u.awsConfig)
	if err != nil {
		return "", 0, wrap("S3", err)
	}

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, wrap("S3", err)
	}
	defer func() {
		_ = file.Close()
	}()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, wrap("S3", err)
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
		return "", 0, wrap("S3", err)
	}

	return fmt.Sprintf("https://%s.s3.amazonaws.com/%s", *u.bucket, storageFilepath), stat.Size(), nil
}
