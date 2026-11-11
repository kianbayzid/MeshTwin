package store

import (
	"context"
	"fmt"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// S3 reads summaries from MinIO or any S3-compatible store. Read-only: unlike
// the ingester it never creates the bucket.
type S3 struct {
	client *minio.Client
	bucket string
}

// NewS3 builds a client; it does not touch the network, so the API starts
// even when storage is down and /readyz reports it.
func NewS3(endpoint, accessKey, secretKey, bucket string, useTLS bool) (*S3, error) {
	c, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: useTLS,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 client: %w", err)
	}
	return &S3{client: c, bucket: bucket}, nil
}

func (s *S3) Get(ctx context.Context, region, name string) ([]byte, error) {
	obj, err := s.client.GetObject(ctx, s.bucket, "summaries/region="+region+"/"+name+".json", minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("get summary: %w", err)
	}
	defer func() { _ = obj.Close() }() // read-only: a close error cannot lose data
	b, err := readCapped(obj)
	if minio.ToErrorResponse(err).Code == minio.NoSuchKey {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read summary: %w", err)
	}
	return b, nil
}

func (s *S3) List(ctx context.Context) (map[string][]string, error) {
	m := map[string][]string{}
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: "summaries/", Recursive: true}) {
		if obj.Err != nil {
			return nil, fmt.Errorf("list summaries: %w", obj.Err)
		}
		addKey(m, obj.Key)
	}
	return m, nil
}
