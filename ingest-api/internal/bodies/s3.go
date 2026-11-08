package bodies

import (
	"bytes"
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

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
