package blobstore

import (
	"context"
	"strings"
	"testing"
)

func TestContentDigestUsesSha256HexPrefix(t *testing.T) {
	got := ContentDigest([]byte("omnara"))
	const want = "sha256:16d1919c1cfbfe9c21ca222b207c04db6e3b0df3808f4f441fe7a0f14de15b83"
	if got != want {
		t.Fatalf("ContentDigest(%q) = %q, want %q", "omnara", got, want)
	}
	empty := ContentDigest(nil)
	const wantEmpty = "sha256:e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if empty != wantEmpty {
		t.Fatalf("ContentDigest(nil) = %q, want %q", empty, wantEmpty)
	}
	if !strings.HasPrefix(empty, "sha256:") || len(empty) != len("sha256:")+64 {
		t.Fatalf("empty digest length = %d", len(empty))
	}
}

func TestNewS3StoreRequiresBucket(t *testing.T) {
	_, err := NewS3Store(context.Background(), S3Config{})
	if err == nil {
		t.Fatal("expected empty bucket to be rejected")
	}
}

func TestS3StorePutBlobRequiresKey(t *testing.T) {
	store := &S3Store{}
	if _, err := store.PutBlob(context.Background(), "", []byte("blob")); err == nil {
		t.Fatal("expected empty blob key to be rejected")
	}
}
