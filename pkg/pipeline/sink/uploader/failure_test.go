package uploader

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/egress/pkg/errors"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/storage"
)

// A failed upload reads the same for every store: its name, then the status
// and the error code it answered with.
func TestUploadErrorMessage(t *testing.T) {
	providers := []struct {
		name string
		conf func(endpoint string) *config.StorageConfig
		// errorDocument is a 403 in the store's own error format.
		errorDocument string
		documented    string
		// undocumented is the message for a 403 in any other format.
		undocumented string
		// unexpected is the message for a 200 the SDK does not take for the
		// upload's success, empty where it does.
		unexpected string
	}{
		{
			name: "S3",
			conf: func(endpoint string) *config.StorageConfig {
				return &config.StorageConfig{S3: &storage.S3Config{
					AccessKey: "test", Secret: "test", Region: "us-east-1", Bucket: "bucket",
					Endpoint: endpoint, ForcePathStyle: true, MaxRetries: 1,
				}}
			},
			errorDocument: `<Error><Code>AccessDenied</Code><Message>Access Denied</Message></Error>`,
			documented:    "S3 upload failed: 403 AccessDenied",
			undocumented:  "S3 upload failed: 403 Forbidden",
		},
		{
			name: "GCP",
			conf: func(endpoint string) *config.StorageConfig {
				return &config.StorageConfig{GCP: &storage.GCPConfig{Bucket: "bucket", Endpoint: endpoint}}
			},
			errorDocument: `{"error":{"code":403,"message":"denied","errors":[{"reason":"forbidden","message":"denied"}]}}`,
			documented:    "GCP upload failed: 403 forbidden",
			undocumented:  "GCP upload failed: 403 Forbidden",
			unexpected:    "GCP upload failed: request failed",
		},
		{
			name: "Azure",
			conf: func(endpoint string) *config.StorageConfig {
				return &config.StorageConfig{Azure: &storage.AzureConfig{
					AccountName: "account", AccountKey: "a2V5", ContainerName: "container", Endpoint: endpoint + "/account/",
				}}
			},
			errorDocument: `<Error><Code>AuthenticationFailed</Code><Message>denied</Message></Error>`,
			documented:    "Azure upload failed: 403 AuthenticationFailed",
			undocumented:  "Azure upload failed: 403 Forbidden",
			unexpected:    "Azure upload failed: unexpected status 200",
		},
		{
			name: "AliOSS",
			conf: func(endpoint string) *config.StorageConfig {
				return &config.StorageConfig{AliOSS: &storage.AliOSSConfig{AccessKey: "key", Secret: "secret", Endpoint: endpoint, Bucket: "bucket"}}
			},
			errorDocument: `<Error><Code>AccessDenied</Code><Message>denied</Message><RequestId>r</RequestId></Error>`,
			documented:    "AliOSS upload failed: 403 AccessDenied",
			// The SDK gives no status for a response it cannot parse.
			undocumented: "AliOSS upload failed: request failed",
		},
	}

	for _, p := range providers {
		t.Run(p.name, func(t *testing.T) {
			for _, r := range []struct {
				name   string
				status int
				body   string
				want   string
			}{
				{name: "error document", status: http.StatusForbidden, body: p.errorDocument, want: p.documented},
				{name: "other body", status: http.StatusForbidden, body: "denied", want: p.undocumented},
				{name: "unexpected 200", status: http.StatusOK, body: "ok", want: p.unexpected},
			} {
				t.Run(r.name, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						w.WriteHeader(r.status)
						_, _ = w.Write([]byte(r.body))
					}))
					defer server.Close()

					err := upload(t, p.conf(server.URL))
					if r.want == "" {
						require.NoError(t, err)
						return
					}
					require.EqualError(t, err, r.want)

					// The SDK's error stays in the chain, which classifies the failure.
					if strings.Contains(r.want, "403") {
						var status *storage.ErrorWithStatusCode
						require.ErrorAs(t, err, &status)
						require.Equal(t, r.status, status.StatusCode)
						require.True(t, errors.IsDestinationError(err))
					}
				})
			}
		})
	}
}

// A request that gets no response is reported by how the connection failed.
func TestUploadErrorMessageNoResponse(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	endpoint := "http://" + listener.Addr().String()
	require.NoError(t, listener.Close())

	err = upload(t, &config.StorageConfig{S3: &storage.S3Config{
		AccessKey: "test", Secret: "test", Region: "us-east-1", Bucket: "bucket",
		Endpoint: endpoint, ForcePathStyle: true, MaxRetries: 1,
	}})
	require.EqualError(t, err, "S3 upload failed: connection failed")
}

// A refused GCS token request is reported like a refused upload.
func TestUploadErrorMessageTokenRequest(t *testing.T) {
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"invalid_grant"}`))
	}))
	defer tokens.Close()
	credentials, err := json.Marshal(map[string]string{
		"type":         "service_account",
		"client_email": "egress@test.iam.gserviceaccount.com",
		"private_key":  ociPrivateKeyPEM(t),
		"token_uri":    tokens.URL,
	})
	require.NoError(t, err)

	err = upload(t, &config.StorageConfig{GCP: &storage.GCPConfig{
		CredentialsJSON: string(credentials), Bucket: "bucket", Endpoint: "https://127.0.0.1:1",
	}})
	require.EqualError(t, err, "GCP upload failed: 400 invalid_grant")
}

func upload(t *testing.T, conf *config.StorageConfig) error {
	u, err := New(conf, nil, nil, nil, &livekit.EgressInfo{})
	require.NoError(t, err)
	_, _, err = u.Upload("uploader_test.go", "uploader_test.go", "text/plain", false)
	return err
}
