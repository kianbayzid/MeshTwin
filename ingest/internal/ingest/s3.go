package ingest

import (
	"bytes"
	"context"
	"fmt"
	"sync/atomic"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3Store writes objects to MinIO, SeaweedFS or any S3-compatible store.
// Safe for concurrent use (the Writer and the spool drainer both call Put).
type S3Store struct {
	client   *minio.Client
	bucket   string
	bucketOK atomic.Bool // set once the bucket is known to exist
}

// NewS3Store builds the client without touching the network, so the
// ingester starts (and spools) even when storage is down. The bucket is
// checked and created on the first Put instead.
func NewS3Store(endpoint, accessKey, secretKey, bucket string, useTLS bool) (*S3Store, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useTLS,
		// Fail fast and let our own layer (spool drainer backoff, or the
		// Writer's 10 s retry) decide when to try again.
		MaxRetries: 1,
	})
	if err != nil {
		return nil, err
	}
	return &S3Store{client: c, bucket: bucket}, nil
}

func (s *S3Store) Put(ctx context.Context, key string, data []byte) error {
	if err := s.ensureBucket(ctx); err != nil {
		return err
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "application/vnd.apache.parquet"})
	return err
}

// ensureBucket creates the bucket if it does not exist yet. Two concurrent
// first calls may both try MakeBucket; the loser re-checks and succeeds.
func (s *S3Store) ensureBucket(ctx context.Context) error {
	if s.bucketOK.Load() {
		return nil
	}
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("check bucket %q: %w", s.bucket, err)
	}
	if !exists {
		if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{}); err != nil {
			if ok, err2 := s.client.BucketExists(ctx, s.bucket); err2 != nil || !ok {
				return fmt.Errorf("create bucket %q: %w", s.bucket, err)
			}
		}
	}
	s.bucketOK.Store(true)
	return nil
}
