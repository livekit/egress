// +build !test

package recorder

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/Azure/azure-storage-blob-go/azblob"
)

func (r *Recorder) uploadAzblob() error {
	credential, err := azblob.NewSharedKeyCredential(
		r.conf.FileOutput.Azblob.AccountName,
		r.conf.FileOutput.Azblob.AccountKey,
	)
	if err != nil {
		return err
	}

	p := azblob.NewPipeline(credential, azblob.PipelineOptions{})

	URL, _ := url.Parse(
		fmt.Sprintf("https://%s.blob.core.windows.net/%s",
			r.conf.FileOutput.Azblob.AccountName,
			r.conf.FileOutput.Azblob.ContainerName,
		),
	)

	containerURL := azblob.NewContainerURL(*URL, p)

	blobURL := containerURL.NewBlockBlobURL(r.filepath)
	file, err := os.Open(r.filename)
	if err != nil {
		return err
	}
	// uploads blocks in parallel for optimal performance
	// it calls PutBlock/PutBlockList for files larger than 256 MBs and PutBlob for smaller files
	ctx := context.Background()
	_, err = azblob.UploadFileToBlockBlob(ctx, file, blobURL, azblob.UploadToBlockBlobOptions{
		BlobHTTPHeaders: azblob.BlobHTTPHeaders{ContentType: "video/mp4"},
		BlockSize:       4 * 1024 * 1024,
		Parallelism:     16,
	})
	return err
}
