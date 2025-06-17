package uploader

import (
	"io"
	"net/http"
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
	u, err := New(primary, backup, nil, info)
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
