package ingest

import (
	"bytes"
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Store writes objects to MinIO or any S3-compatible store.
type S3Store struct {
	client *minio.Client
	bucket string
}

// NewS3Store connects and creates the bucket if it does not exist yet.
func NewS3Store(ctx context.Context, endpoint, accessKey, secretKey, bucket string, useTLS bool) (*S3Store, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useTLS,
	})
	if err != nil {
		return nil, err
	}
	s := &S3Store{client: c, bucket: bucket}
	exists, err := c.BucketExists(ctx, bucket)
	if err != nil {
		return nil, fmt.Errorf("check bucket %q: %w", bucket, err)
	}
	if !exists {
		if err := c.MakeBucket(ctx, bucket, minio.MakeBucketOptions{}); err != nil {
			return nil, fmt.Errorf("create bucket %q: %w", bucket, err)
		}
	}
	return s, nil
}

func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/vnd.apache.parquet"})
	return err
}

// Ping checks the store is reachable (used by the readiness probe).
func (s *S3Store) Ping(ctx context.Context) error {
	_, err := s.client.BucketExists(ctx, s.bucket)
	return err
}
