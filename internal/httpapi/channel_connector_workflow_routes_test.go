package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

type channelWorkflowHTTPBlobs struct {
	blobstore.Store // Unexpected blob reads panic; incoming preparation only uploads/deletes.
	mu              sync.Mutex
	content         map[string][]byte
	puts, deletes   []string
	putHook         func(context.Context, string, []byte) error
}

func (b *channelWorkflowHTTPBlobs) PutBlob(
	ctx context.Context, key string, content []byte,
) (blobstore.Metadata, error) {
	if b.putHook != nil {
		if err := b.putHook(ctx, key, content); err != nil {
			return blobstore.Metadata{}, err
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.content == nil {
		b.content = make(map[string][]byte)
	}
	b.content[key] = append([]byte(nil), content...)
	b.puts = append(b.puts, key)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *channelWorkflowHTTPBlobs) DeleteBlob(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.content, key)
	b.deletes = append(b.deletes, key)
	return nil
}

func (b *channelWorkflowHTTPBlobs) snapshot() (uploads, deletions []string, retained int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.puts...), append([]string(nil), b.deletes...), len(b.content)
}

func TestChannelConnectorDefinitionKindMatchesRealProvider(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		provider string
		kind     openapi.ChannelKind
		want     bool
	}{
		{"slack", openapi.ChannelKindSlackChannel, true},
		{"slack", openapi.ChannelKindSlackThread, true},
		{"slack", openapi.ChannelKindExternal, false},
		{"discord", openapi.ChannelKindExternal, false},
		{"discord", openapi.ChannelKindDiscordChannel, true},
		{"discord", openapi.ChannelKindDiscordThread, true},
		{"github", openapi.ChannelKindGitHubPR, true},
		{"github", openapi.ChannelKindGitHubReviewThread, true},
		{"github", openapi.ChannelKindDiscordThread, false},
		{"discord", openapi.ChannelKindSlackThread, false},
		{"telegram", openapi.ChannelKindSlackChannel, false},
		{"discord", "NEW_CUSTOM_KIND", false},
	} {
		t.Run(tc.provider+"/"+string(tc.kind), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, integrationstore.ChannelKind(tc.kind).MatchesProvider(tc.provider))
		})
	}
}

func TestChannelConnectorInputMediaPreparesWithoutDatabaseAndCleansPartialFailure(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	blobs := &channelWorkflowHTTPBlobs{}
	store := storage.NewStore(nil, storage.WithBlobStore(blobs))
	server := &Server{store: store}
	plan, err := preflightInlineMedia(json.RawMessage(`[
{"type":"text","text":"see files"},
{"type":"media","media_type":"image/png","data":"b25l","metadata":{"caption":"first"}},
{"type":"media","media_type":"image/png","data":"dHdv"}]`), inlineMediaAgentInput, maxContentBlocksPerInput)
	require.NoError(t, err)
	ingest := mediaIngestContext{ProjectID: uuid.New(), AgentID: uuid.New(), IdempotencyKey: "receipt:test"}
	uploadErr := errors.New("upload unavailable")
	blobs.putHook = func(_ context.Context, _ string, content []byte) error {
		if string(content) == "two" {
			return uploadErr
		}
		return nil
	}
	_, err = server.prepareChannelInputMedia(ctx, ingest, plan)
	require.ErrorIs(t, err, uploadErr)
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 1)
	require.Equal(t, uploads, deletions, "failure before delivery must compensate the successful earlier upload")
	require.Zero(t, retained)

	blobs.putHook = nil
	content, err := server.prepareChannelInputMedia(ctx, ingest, plan)
	require.NoError(t, err, "preallocated agent requires no database for preparation")
	require.Len(t, content.Attachments, 2)
	require.Equal(t, 1, content.Attachments[0].Ordinal)
	require.JSONEq(t, `{"caption":"first"}`, string(content.Attachments[0].Metadata))
	require.NotContains(t, string(content.Blocks), "b25l", "transaction input must not keep duplicate base64 bytes")
	require.NoError(t, store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack,
		content.Attachments[0].Artifact, content.Attachments[1].Artifact))
	uploads, deletions, retained = blobs.snapshot()
	require.Equal(t, uploads, deletions)
	require.Zero(t, retained)
}

func TestChannelConnectorWorkflowBodyLimitUsesExistingAttachmentBudget(t *testing.T) {
	t.Parallel()
	base := "/api/v1/channel-connector/apps/iapp_test/installations/iin_test/"
	for _, tc := range []struct {
		path string
		want int64
	}{
		{base + "workflows/deliver", maxAttachmentRequestBodyBytes},
		{base + "channels/deliver", maxAttachmentRequestBodyBytes},
		{base + "channels/recipients", maxRequestBodyBytes},
		{base + "channel-definitions/publish", maxRequestBodyBytes},
	} {
		t.Run(tc.path, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(`{}`))
			require.Equal(t, tc.want, requestBodyLimit(request))
		})
	}
}
