package bodies

import (
	"bytes"
	"context"
	"errors"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrBodyNotFound = errors.New("body not found")

type S3Uploader struct {
	client *s3.Client
	bucket string
}

func NewS3Uploader(endpoint string, bucket string, region string, accessKeyID string, secretAccessKey string) *S3Uploader {
	client := s3.New(s3.Options{
		Region:           region,
		BaseEndpoint:     aws.String(endpoint),
		UsePathStyle:     true,
		Credentials:      credentials.NewStaticCredentialsProvider(accessKeyID, secretAccessKey, ""),
		RetryMaxAttempts: 2,
	})
	return &S3Uploader{client: client, bucket: bucket}
}

func (uploader *S3Uploader) Put(ctx context.Context, key string, gzippedBody []byte) error {
	_, err := uploader.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket: aws.String(uploader.bucket),
		Key:    aws.String(key),
		Body:   bytes.NewReader(gzippedBody),
	})
	return err
}

func (uploader *S3Uploader) Get(ctx context.Context, key string) ([]byte, error) {
	output, err := uploader.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(uploader.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, ErrBodyNotFound
		}
		return nil, err
	}
	defer output.Body.Close()
	return io.ReadAll(output.Body)
}
