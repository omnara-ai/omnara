package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/stretchr/testify/require"
)

func testS3StreamStore(t *testing.T, handler http.HandlerFunc) *S3Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &S3Store{
		bucket: "test-bucket",
		client: s3.NewFromConfig(aws.Config{
			Region: "us-east-1", Credentials: aws.AnonymousCredentials{}, RetryMaxAttempts: 1,
		}, func(options *s3.Options) {
			options.BaseEndpoint = aws.String(server.URL)
			options.UsePathStyle = true
		}),
	}
}

func TestOpenBlobStreamsBeforeEOFWithoutIncomingSizeCap(t *testing.T) {
	t.Parallel()
	prefix := []byte("first bytes")
	chunk := bytes.Repeat([]byte("x"), 32*1024)
	const chunks = 384 // 12 MiB: the incoming-inline cap is not an egress limit.
	size := len(prefix) + chunks*len(chunk)
	expectedHash := sha256.New()
	_, err := expectedHash.Write(prefix)
	require.NoError(t, err)
	for range chunks {
		_, err := expectedHash.Write(chunk)
		require.NoError(t, err)
	}
	digest := "sha256:" + hex.EncodeToString(expectedHash.Sum(nil))
	releaseTail := make(chan struct{})
	store := testS3StreamStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/test-bucket/large-object" {
			t.Error("unexpected object request")
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Length", strconv.Itoa(size))
		w.Header().Set("X-Amz-Meta-Omnara-Digest", digest)
		if _, err := w.Write(prefix); err != nil {
			return
		}
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		select {
		case <-releaseTail:
		case <-r.Context().Done():
			return
		}
		for range chunks {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body, metadata, err := store.OpenBlob(ctx, "large-object")
	require.NoError(t, err, "open must return while the server is withholding the rest of the object")
	defer func() { require.NoError(t, body.Close()) }()
	require.Equal(t, Metadata{Digest: digest, SizeBytes: int64(size)}, metadata)
	first := make([]byte, len(prefix))
	_, err = io.ReadFull(body, first)
	require.NoError(t, err)
	require.Equal(t, prefix, first)
	close(releaseTail)
	actualHash := sha256.New()
	_, err = actualHash.Write(first)
	require.NoError(t, err)
	n, err := io.Copy(actualHash, body)
	require.NoError(t, err)
	require.Equal(t, int64(size-len(first)), n)
	require.Equal(t, expectedHash.Sum(nil), actualHash.Sum(nil))
}

func TestOpenBlobCancellationAndCloseInterruptActualRead(t *testing.T) {
	t.Parallel()
	for _, action := range []string{"cancel", "close"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			disconnected := make(chan struct{})
			store := testS3StreamStore(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "100")
				w.Header().Set("X-Amz-Meta-Omnara-Digest", ContentDigest(nil))
				if err := http.NewResponseController(w).Flush(); err != nil {
					return
				}
				<-r.Context().Done()
				close(disconnected)
			})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			body, _, err := store.OpenBlob(ctx, "blocked-object")
			require.NoError(t, err)
			defer func() { _ = body.Close() }()
			readDone := make(chan error, 1)
			go func() {
				_, err := body.Read(make([]byte, 1))
				readDone <- err
			}()
			if action == "cancel" {
				cancel()
			} else {
				require.NoError(t, body.Close())
			}
			select {
			case err := <-readDone:
				require.Error(t, err)
				if action == "cancel" {
					require.ErrorIs(t, err, context.Canceled)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("read remained blocked")
			}
			select {
			case <-disconnected:
			case <-time.After(2 * time.Second):
				t.Fatal("storage HTTP request remained live")
			}
		})
	}
}

func TestOpenBlobCancellationInterruptsOpening(t *testing.T) {
	t.Parallel()
	started := make(chan struct{})
	store := testS3StreamStore(t, func(_ http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		body, _, err := store.OpenBlob(ctx, "no-headers")
		if body != nil {
			_ = body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("storage request did not start")
	}
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(2 * time.Second):
		t.Fatal("opening remained blocked")
	}
}

func TestOpenBlobRejectsMissingMetadataAndClosesBody(t *testing.T) {
	t.Parallel()
	disconnected := make(chan struct{})
	store := testS3StreamStore(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "100")
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		<-r.Context().Done()
		close(disconnected)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	body, _, err := store.OpenBlob(ctx, "missing-digest")
	require.Nil(t, body)
	require.ErrorContains(t, err, "missing the omnara-digest")
	select {
	case <-disconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("rejected response body was not closed")
	}
}

func TestGetBlobStillReturnsBytesAndActualSize(t *testing.T) {
	t.Parallel()
	content := []byte("existing buffered consumers")
	store := testS3StreamStore(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-Amz-Meta-Omnara-Digest", ContentDigest(content))
		// Flush headers first so length is unknown until the body is read.
		if err := http.NewResponseController(w).Flush(); err != nil {
			return
		}
		_, _ = w.Write(content)
	})
	got, metadata, err := store.GetBlob(t.Context(), "buffered-object")
	require.NoError(t, err)
	require.Equal(t, content, got)
	require.Equal(t, Metadata{Digest: ContentDigest(content), SizeBytes: int64(len(content))}, metadata)
	body, metadata, err := store.OpenBlob(t.Context(), "buffered-object")
	require.NoError(t, err)
	defer func() { require.NoError(t, body.Close()) }()
	require.Equal(t, int64(-1), metadata.SizeBytes)
}
