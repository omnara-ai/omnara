//go:build integration

package blobstore_test

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/stretchr/testify/require"
)

func TestS3BlobRoundTrip(t *testing.T) {
	store := integrationblob.MustOpen(t, t.Context())
	for _, test := range []struct {
		name    string
		content []byte
	}{
		{"empty", []byte{}},
		{"binary", []byte{0, 255, 1, 2, '\r', '\n'}},
		{"large", bytes.Repeat([]byte("binary\x00content\r\n"), 800000)},
	} {
		t.Run(test.name, func(t *testing.T) {
			key := "integration/" + uuid.NewString()
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				_ = store.DeleteBlob(ctx, key)
			})
			_, _, err := store.GetBlob(t.Context(), key)
			require.ErrorIs(t, err, blobstore.ErrNotFound)
			for _, content := range [][]byte{test.content, []byte("replacement")} {
				want := blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}
				metadata, err := store.PutBlob(t.Context(), key, content)
				require.NoError(t, err)
				require.Equal(t, want, metadata)
				body, metadata, err := store.GetBlob(t.Context(), key)
				require.NoError(t, err)
				require.Equal(t, content, body)
				require.Equal(t, want, metadata)
			}
			for range 2 {
				require.NoError(t, store.DeleteBlob(t.Context(), key))
			}
			_, _, err = store.GetBlob(t.Context(), key)
			require.ErrorIs(t, err, blobstore.ErrNotFound)
		})
	}
}
