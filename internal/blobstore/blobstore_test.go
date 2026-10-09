package blobstore

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func s3HTTPStore(t *testing.T, handler http.HandlerFunc) *S3Store {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	store, err := NewS3Store(t.Context(), S3Config{
		Bucket: "test", Region: "us-east-1", Endpoint: server.URL,
		AccessKeyID: "test", SecretAccessKey: "test", UsePathStyle: true,
	})
	require.NoError(t, err)
	return store
}

func TestS3GetBlobMissingKeyPermissions(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"NoSuchKey", http.StatusNotFound},
		{"AccessDenied", http.StatusForbidden},
		{"NoSuchBucket", http.StatusNotFound},
	} {
		t.Run(tc.code, func(t *testing.T) {
			store := s3HTTPStore(t, func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodGet, r.Method)
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(tc.status)
				_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>denied or absent</Message></Error>", tc.code)
			})
			_, _, err := store.GetBlob(t.Context(), "artifacts/agent/missing")
			if tc.code == "NoSuchKey" {
				require.ErrorIs(t, err, ErrNotFound)
			} else {
				require.NotErrorIs(t, err, ErrNotFound)
				var apiError smithy.APIError
				require.ErrorAs(t, err, &apiError)
				require.Equal(t, tc.code, apiError.ErrorCode())
			}
		})
	}
}

func TestS3PutBlob(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
	}{
		{"created", http.StatusOK},
		{"PreconditionFailed", http.StatusPreconditionFailed},
		{"AccessDenied", http.StatusForbidden},
		{"ConditionalRequestConflict", http.StatusConflict},
		{"NotImplemented", http.StatusNotImplemented},
	} {
		t.Run(tc.code, func(t *testing.T) {
			content := []byte("pinned content")
			store := s3HTTPStore(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					assert.Equal(t, "PreconditionFailed", tc.code)
					existing := []byte("different content")
					w.Header().Set("X-Amz-Meta-Omnara-Digest", ContentDigest(content))
					_, _ = w.Write(existing)
					return
				}
				assert.Equal(t, http.MethodPut, r.Method)
				assert.Equal(t, "/test/artifacts/agent/file", r.URL.Path)
				assert.Equal(t, "*", r.Header.Get("If-None-Match"))
				assert.Equal(t, ContentDigest(content), r.Header.Get("X-Amz-Meta-Omnara-Digest"))
				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.Equal(t, content, body)
				w.Header().Set("Content-Type", "application/xml")
				w.Header().Set("X-Amz-Request-Id", "test-request")
				w.WriteHeader(tc.status)
				if tc.status != http.StatusOK {
					_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code></Error>", tc.code)
				}
			})
			metadata, err := store.PutBlob(t.Context(), "artifacts/agent/file", content)
			switch tc.code {
			case "created":
				require.NoError(t, err)
				require.Equal(t, Metadata{Digest: ContentDigest(content), SizeBytes: int64(len(content))}, metadata)
			case "PreconditionFailed":
				require.ErrorIs(t, err, ErrContentConflict)
				var apiError smithy.APIError
				require.ErrorAs(t, err, &apiError)
				require.Equal(t, tc.code, apiError.ErrorCode())
				require.ErrorContains(t, err, "test-request")
			default:
				require.NotErrorIs(t, err, ErrContentConflict)
				var apiError smithy.APIError
				require.ErrorAs(t, err, &apiError)
				require.Equal(t, tc.code, apiError.ErrorCode())
			}
		})
	}
}

func TestS3PutBlobRecoversCommittedWrite(t *testing.T) {
	content := []byte("committed bytes")
	var puts atomic.Int32
	store := s3HTTPStore(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("X-Amz-Meta-Omnara-Digest", ContentDigest(content))
			_, _ = w.Write(content)
			return
		}
		assert.Equal(t, http.MethodPut, r.Method)
		assert.Equal(t, "*", r.Header.Get("If-None-Match"))
		w.Header().Set("Content-Type", "application/xml")
		if puts.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte("<Error><Code>InternalError</Code></Error>"))
		} else {
			w.WriteHeader(http.StatusPreconditionFailed)
			_, _ = w.Write([]byte("<Error><Code>PreconditionFailed</Code></Error>"))
		}
	})
	metadata, err := store.PutBlob(t.Context(), "artifacts/agent/file", content)
	require.NoError(t, err)
	require.EqualValues(t, 2, puts.Load())
	require.Equal(t, Metadata{Digest: ContentDigest(content), SizeBytes: int64(len(content))}, metadata)
}
