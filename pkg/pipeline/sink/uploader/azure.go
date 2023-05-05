package uploader

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/Azure/azure-storage-blob-go/azblob"

	"github.com/livekit/egress/pkg/types"
	"github.com/livekit/protocol/livekit"
)

type AzureUploader struct {
	conf      *livekit.AzureBlobUpload
	container string
}

func newAzureUploader(conf *livekit.AzureBlobUpload) (uploader, error) {
	return &AzureUploader{
		conf:      conf,
		container: fmt.Sprintf("https://%s.blob.core.windows.net/%s", conf.AccountName, conf.ContainerName),
	}, nil
}

func (u *AzureUploader) upload(localFilepath, storageFilepath string, outputType types.OutputType) (string, int64, error) {
	credential, err := azblob.NewSharedKeyCredential(
		u.conf.AccountName,
		u.conf.AccountKey,
	)
	if err != nil {
		return "", 0, err
	}

	azUrl, err := url.Parse(u.container)
	if err != nil {
		return "", 0, err
	}

	pipeline := azblob.NewPipeline(credential, azblob.PipelineOptions{
		Retry: azblob.RetryOptions{
			Policy:        azblob.RetryPolicyExponential,
			MaxTries:      maxRetries,
			RetryDelay:    minDelay,
			MaxRetryDelay: maxDelay,
		},
	})
	containerURL := azblob.NewContainerURL(*azUrl, pipeline)
	blobURL := containerURL.NewBlockBlobURL(storageFilepath)

	file, err := os.Open(localFilepath)
	if err != nil {
		return "", 0, err
	}
	defer func() {
		_ = file.Close()
	}()

	stat, err := file.Stat()
	if err != nil {
		return "", 0, err
	}

	// upload blocks in parallel for optimal performance
	// it calls PutBlock/PutBlockList for files larger than 256 MBs and PutBlob for smaller files
	_, err = azblob.UploadFileToBlockBlob(context.Background(), file, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: azblob.BlobHTTPHeaders{ContentType: string(outputType)},
		BlockSize:       4 * 1024 * 1024,
		Parallelism:     16,
	})
	if err != nil {
		return "", 0, err
	}

	return fmt.Sprintf("%s/%s", u.container, storageFilepath), stat.Size(), nil
}
