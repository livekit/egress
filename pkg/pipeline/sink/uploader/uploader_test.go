package uploader

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
	"github.com/livekit/protocol/livekit"
	"github.com/livekit/storage"
)

func TestUploader(t *testing.T) {
	key := os.Getenv("AWS_ACCESS_KEY")
	secret := os.Getenv("AWS_SECRET")
	region := os.Getenv("AWS_REGION")
	bucket := os.Getenv("AWS_BUCKET")
	if key == "" || secret == "" || region == "" || bucket == "" {
		t.Skip("uploads to a real bucket; set AWS_ACCESS_KEY, AWS_SECRET, AWS_REGION and AWS_BUCKET to run")
	}

	primary := &config.StorageConfig{
		S3: &storage.S3Config{
			AccessKey: "nonsense",
			Secret:    "public",
			Region:    "us-east-1",
			Bucket:    "fake-bucket",
		},
	}
	backup := &config.StorageConfig{
		Prefix: "testProject",
		S3: &storage.S3Config{
			AccessKey: key,
			Secret:    secret,
			Region:    region,
			Bucket:    bucket,
		},
		GeneratePresignedUrl: true,
	}

	info := &livekit.EgressInfo{}
	u, err := New(primary, backup, nil, nil, info)
	require.NoError(t, err)

	filepath := "uploader_test.go"
	storagePath := "uploader_test.go"

	location, size, err := u.Upload(filepath, storagePath, "text/plain", false)
	require.NoError(t, err)

	require.NotZero(t, size)
	require.NotEmpty(t, location)
	require.True(t, info.BackupStorageUsed)

	response, err := http.Get(location)
	require.NoError(t, err)
	defer response.Body.Close()

	require.Equal(t, http.StatusOK, response.StatusCode)
	b, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(string(b), "package uploader"))
}

func TestHasCustomEndpoint(t *testing.T) {
	ociKey := ociPrivateKeyPEM(t)
	// Namespace and the api key fields keep NewOCI off the network and off ~/.oci/config.
	ociConf := func(endpoint string) *storage.OCIConfig {
		return &storage.OCIConfig{
			TenancyOCID: "ocid1.tenancy.oc1..tenancy",
			UserOCID:    "ocid1.user.oc1..user",
			Fingerprint: "aa:bb:cc",
			PrivateKey:  ociKey,
			Region:      "us-ashburn-1",
			Namespace:   "testnamespace",
			Bucket:      "fake-bucket",
			Endpoint:    endpoint,
		}
	}

	cases := []struct {
		name string
		conf *config.StorageConfig
		want bool
	}{
		{
			name: "s3 without endpoint",
			conf: &config.StorageConfig{S3: &storage.S3Config{Region: "us-east-1", Bucket: "b"}},
			want: false,
		},
		{
			name: "s3 with endpoint",
			conf: &config.StorageConfig{S3: &storage.S3Config{Region: "us-east-1", Bucket: "b", Endpoint: "https://minio.example.com"}},
			want: true,
		},
		{
			name: "alioss without endpoint",
			conf: &config.StorageConfig{AliOSS: &storage.AliOSSConfig{Bucket: "fake-bucket"}},
			want: false,
		},
		{
			name: "alioss with endpoint",
			conf: &config.StorageConfig{AliOSS: &storage.AliOSSConfig{Bucket: "fake-bucket", Endpoint: "oss-cn-hangzhou.aliyuncs.com"}},
			want: true,
		},
		{
			name: "oci without endpoint",
			conf: &config.StorageConfig{OCI: ociConf("")},
			want: false,
		},
		{
			name: "oci with endpoint",
			conf: &config.StorageConfig{OCI: ociConf("objectstorage.us-ashburn-1.oraclecloud.com")},
			want: true,
		},
		{
			name: "azure",
			conf: &config.StorageConfig{Azure: &storage.AzureConfig{AccountName: "n", AccountKey: "a2V5", ContainerName: "c"}},
			want: false,
		},
		{
			name: "local",
			conf: &config.StorageConfig{},
			want: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := getUploader(tc.conf)
			require.NoError(t, err)
			require.Equal(t, tc.want, s.hasCustomEndpoint)
		})
	}
}

// NewOCI parses the key while building the client, so a placeholder PEM won't do.
func ociPrivateKeyPEM(t *testing.T) string {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}))
}

func TestUploadErrorHasStatusCode(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
	}{
		{"forbidden", http.StatusForbidden},
		{"internal server error", http.StatusInternalServerError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer server.Close()

			primary := &config.StorageConfig{
				S3: &storage.S3Config{
					AccessKey:      "test",
					Secret:         "test",
					Region:         "us-east-1",
					Bucket:         "test-bucket",
					Endpoint:       server.URL,
					ForcePathStyle: true,
					MaxRetries:     1,
				},
			}

			u, err := New(primary, nil, nil, nil, &livekit.EgressInfo{})
			require.NoError(t, err)

			_, _, err = u.Upload("uploader_test.go", "uploader_test.go", "text/plain", false)
			require.Error(t, err)

			var statusErr *storage.ErrorWithStatusCode
			require.ErrorAs(t, err, &statusErr)
			require.Equal(t, tc.statusCode, statusErr.StatusCode)
		})
	}
}
