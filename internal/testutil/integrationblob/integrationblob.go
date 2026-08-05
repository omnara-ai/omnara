// Package integrationblob opens the MinIO-backed blob store from
// compose.yaml for integration tests, mirroring how integrationdb opens
// the test database.
package integrationblob

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/omnara-ai/omnara/internal/blobstore"
)

func open(ctx context.Context) (*blobstore.S3Store, error) {
	store, err := blobstore.NewS3Store(ctx, blobstore.S3Config{
		Bucket:          getenv("OMNARA_TEST_S3_BUCKET", "omnara-test"),
		Region:          getenv("OMNARA_TEST_S3_REGION", "us-east-1"),
		Endpoint:        getenv("OMNARA_TEST_S3_ENDPOINT", "http://127.0.0.1:59000"),
		AccessKeyID:     getenv("OMNARA_TEST_S3_ACCESS_KEY_ID", "omnara"),
		SecretAccessKey: getenv("OMNARA_TEST_S3_SECRET_ACCESS_KEY", "omnara-blobs"),
		UsePathStyle:    true,
	})
	if err != nil {
		return nil, fmt.Errorf("open test blob store: %w", err)
	}
	return store, nil
}

func MustOpen(t testing.TB, ctx context.Context) *blobstore.S3Store {
	t.Helper()
	store, err := open(ctx)
	if err != nil {
		t.Fatalf("open test blob store (is the compose minio service up? make db-up): %v", err)
	}
	return store
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
