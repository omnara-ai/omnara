package blobstore

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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
		{"NoSuchKey", http.StatusNotFound},     // Missing key with ListBucket.
		{"AccessDenied", http.StatusForbidden}, // Missing key without ListBucket, or a real denial.
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

func TestS3ConditionalPut(t *testing.T) {
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
			metadata, err := store.PutBlobIfAbsent(t.Context(), "artifacts/agent/file", content)
			switch tc.code {
			case "created":
				require.NoError(t, err)
				require.Equal(t, Metadata{Digest: ContentDigest(content), SizeBytes: int64(len(content))}, metadata)
			case "PreconditionFailed":
				require.ErrorIs(t, err, ErrAlreadyExists)
				var apiError smithy.APIError
				require.ErrorAs(t, err, &apiError)
				require.Equal(t, tc.code, apiError.ErrorCode())
				require.ErrorContains(t, err, "test-request")
			default:
				require.NotErrorIs(t, err, ErrAlreadyExists)
				var apiError smithy.APIError
				require.ErrorAs(t, err, &apiError)
				require.Equal(t, tc.code, apiError.ErrorCode())
			}
		})
	}
}
