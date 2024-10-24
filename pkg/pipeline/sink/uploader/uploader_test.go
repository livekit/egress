package uploader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/livekit/egress/pkg/config"
)

func TestUploader(t *testing.T) {
	key := os.Getenv("AWS_ACCESS_KEY")
	secret := os.Getenv("AWS_SECRET")
	region := os.Getenv("AWS_REGION")
	bucket := os.Getenv("AWS_BUCKET")

	primary := &config.StorageConfig{
		S3: &config.S3Config{
			AccessKey: "nonsense",
			Secret:    "public",
			Region:    "us-east-1",
			Bucket:    "fake-bucket",
		},
	}
	backup := &config.StorageConfig{
		PathPrefix: "testProject",
		S3: &config.S3Config{
			AccessKey: key,
			Secret:    secret,
			Region:    region,
			Bucket:    bucket,
		},
		GeneratePresignedUrl: true,
	}

	u, err := New(primary, backup, nil)
	require.NoError(t, err)

	filepath := "uploader_test.go"
	storagePath := "uploader_test.go"

	location, size, presignedUrl, err := u.Upload(filepath, storagePath, "test/plain", false)
	require.NoError(t, err)

	expectedLocation := fmt.Sprintf("https://%s.s3.amazonaws.com/testProject/uploader_test.go", bucket)

	require.Equal(t, expectedLocation, location)
	require.NotZero(t, size)
	require.NotEmpty(t, presignedUrl)

	response, err := http.Get(presignedUrl)
	require.NoError(t, err)
	defer response.Body.Close()

	require.Equal(t, http.StatusOK, response.StatusCode)
	b, err := io.ReadAll(response.Body)
	require.NoError(t, err)

	require.True(t, strings.HasPrefix(string(b), "package uploader"))
}
