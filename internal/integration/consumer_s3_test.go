package integration

import (
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"github.com/aws/smithy-go"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type inboxS3Options struct {
	listBucket bool
	denyGet    bool
	denyPut    bool
}

type inboxS3Object struct {
	content []byte
	digest  string
}

type inboxS3Server struct {
	mu       sync.Mutex
	objects  map[string]inboxS3Object
	requests []string
}

func (s *inboxS3Server) snapshot() (map[string]inboxS3Object, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.objects), slices.Clone(s.requests)
}

func newInboxS3Store(t *testing.T, options inboxS3Options) (*blobstore.S3Store, *inboxS3Server) {
	t.Helper()
	state := &inboxS3Server{objects: map[string]inboxS3Object{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state.mu.Lock()
		defer state.mu.Unlock()
		state.requests = append(state.requests, r.Method)
		object, exists := state.objects[r.URL.Path]
		fail := func(status int, code string) {
			w.Header().Set("Content-Type", "application/xml")
			w.Header().Set("X-Amz-Request-Id", fmt.Sprintf("test-%s-%d", r.Method, len(state.requests)))
			w.WriteHeader(status)
			_, _ = fmt.Fprintf(w, "<Error><Code>%s</Code><Message>%s</Message></Error>", code, code)
		}
		switch r.Method {
		case http.MethodGet:
			if options.denyGet || (!exists && !options.listBucket) {
				fail(http.StatusForbidden, "AccessDenied")
			} else if !exists {
				fail(http.StatusNotFound, "NoSuchKey")
			} else {
				w.Header().Set("X-Amz-Meta-Omnara-Digest", object.digest)
				_, _ = w.Write(object.content)
			}
		case http.MethodPut:
			if options.denyPut {
				fail(http.StatusForbidden, "AccessDenied")
				return
			}
			assert.Equal(t, "*", r.Header.Get("If-None-Match"))
			if exists {
				fail(http.StatusPreconditionFailed, "PreconditionFailed")
				return
			}
			content, err := io.ReadAll(r.Body)
			assert.NoError(t, err)
			state.objects[r.URL.Path] = inboxS3Object{content: content, digest: r.Header.Get("X-Amz-Meta-Omnara-Digest")}
		default:
			t.Errorf("unexpected S3 method %s", r.Method)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	store, err := blobstore.NewS3Store(t.Context(), blobstore.S3Config{
		Bucket: "test", Region: "us-east-1", Endpoint: server.URL,
		AccessKeyID: "test", SecretAccessKey: "test", UsePathStyle: true,
	})
	require.NoError(t, err)
	return store, state
}

func TestIntegrationConsumerS3PreparedUploads(t *testing.T) {
	for _, listBucket := range []bool{false, true} {
		for _, cached := range []bool{false, true} {
			t.Run(fmt.Sprintf("list_bucket=%t/cached=%t", listBucket, cached), func(t *testing.T) {
				blobs, state := newInboxS3Store(t, inboxS3Options{listBucket: listBucket})
				artifacts := artifactstore.New(nil, blobs)
				file := IntegrationInboxFile{Content: []byte("pinned image bytes"), ContentType: "image/png", Filename: "image.png"}
				expected := artifactstore.PreparedArtifact{
					ID: uuid.Must(uuid.NewV7()), ContentType: file.ContentType, Filename: file.Filename,
					Digest: blobstore.ContentDigest(file.Content), SizeBytes: int64(len(file.Content)),
				}
				recipient := IntegrationInboxRecipient{AgentID: uuid.Must(uuid.NewV7()), ArtifactIDs: []uuid.UUID{expected.ID}}
				message := executionstore.InboxMessage{
					ContentBlocks: []byte(`[{"type":"media_ref","artifact_id":"` + expected.ID.String() + `"}]`),
					Files: []executionstore.InboxPlannedFile{{
						ArtifactID: expected.ID, ProviderFileID: "F123", Expected: &expected,
					}},
				}
				cache := map[string]IntegrationInboxFile{}
				if cached {
					cache["F123"] = file
				}
				provider := &integrationConsumerProvider{file: file}
				consumer := NewIntegrationInboxConsumer(nil, nil, artifacts, nil, nil, nil)
				prepared, err := consumer.prepareFiles(t.Context(), provider, integrationstore.IntegrationRecord{}, nil,
					message, recipient, cache)
				require.NoError(t, err)
				require.Equal(t, []artifactstore.PreparedArtifact{expected}, prepared)
				objects, requests := state.snapshot()
				if cached {
					require.Zero(t, provider.downloads)
					require.Equal(t, []string{http.MethodPut}, requests)
				} else {
					require.Equal(t, 1, provider.downloads)
					require.Equal(t, []string{http.MethodGet, http.MethodPut}, requests)
				}
				key := "/test/artifacts/" + recipient.AgentID.String() + "/" + expected.ID.String()
				require.Equal(t, inboxS3Object{content: file.Content, digest: expected.Digest}, objects[key])

				prepared, err = consumer.prepareFiles(t.Context(), nil, integrationstore.IntegrationRecord{}, nil,
					message, recipient, nil)
				require.NoError(t, err)
				require.Equal(t, []artifactstore.PreparedArtifact{expected}, prepared)
			})
		}
	}
}

func TestIntegrationConsumerS3AuthorizationFailures(t *testing.T) {
	for _, options := range []inboxS3Options{{denyPut: true}, {denyGet: true}} {
		t.Run(fmt.Sprintf("deny_get=%t", options.denyGet), func(t *testing.T) {
			blobs, state := newInboxS3Store(t, options)
			artifacts := artifactstore.New(nil, blobs)
			file := IntegrationInboxFile{Content: []byte("image"), ContentType: "image/png", Filename: "image.png"}
			expected := artifactstore.PreparedArtifact{
				ID: uuid.Must(uuid.NewV7()), ContentType: file.ContentType, Filename: file.Filename,
				Digest: blobstore.ContentDigest(file.Content), SizeBytes: int64(len(file.Content)),
			}
			recipient := IntegrationInboxRecipient{AgentID: uuid.Must(uuid.NewV7()), ArtifactIDs: []uuid.UUID{expected.ID}}
			message := executionstore.InboxMessage{
				ContentBlocks: []byte(`[{"type":"media_ref","artifact_id":"` + expected.ID.String() + `"}]`),
				Files: []executionstore.InboxPlannedFile{{
					ArtifactID: expected.ID, ProviderFileID: "F123", Expected: &expected,
				}},
			}
			if options.denyGet {
				require.NoError(t, artifacts.UploadPreparedArtifact(t.Context(), recipient.AgentID, expected, file.Content))
			}
			consumer := NewIntegrationInboxConsumer(nil, nil, artifacts, nil, nil, nil)
			prepared, err := consumer.prepareFiles(t.Context(), &integrationConsumerProvider{file: file},
				integrationstore.IntegrationRecord{}, nil, message, recipient, nil)
			require.Empty(t, prepared)
			require.NotErrorIs(t, err, blobstore.ErrNotFound)
			var apiError smithy.APIError
			require.ErrorAs(t, err, &apiError)
			require.Equal(t, "AccessDenied", apiError.ErrorCode())
			if options.denyGet {
				require.ErrorIs(t, err, blobstore.ErrAlreadyExists)
				require.ErrorContains(t, err, "test-PUT-3")
				require.ErrorContains(t, err, "test-GET-4")
			}
			if options.denyPut {
				objects, _ := state.snapshot()
				require.Empty(t, objects)
			}
		})
	}
}
