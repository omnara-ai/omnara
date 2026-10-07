package integration

import (
	"context"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type integrationConsumerUploads struct {
	present bool
	probed  []uuid.UUID
	uploads int
	content []byte
}

func (s *integrationConsumerUploads) PreparedArtifactUploaded(
	_ context.Context,
	_ uuid.UUID,
	prepared artifactstore.PreparedArtifact,
) (bool, error) {
	s.probed = append(s.probed, prepared.ID)
	return s.present, nil
}

func (s *integrationConsumerUploads) UploadPreparedArtifact(
	_ context.Context,
	_ uuid.UUID,
	_ artifactstore.PreparedArtifact,
	content []byte,
) error {
	s.uploads++
	s.content = append([]byte(nil), content...)
	return nil
}

type integrationConsumerProvider struct {
	IntegrationInboxProvider
	downloads  int
	file       IntegrationInboxFile
	expansions int
	event      *IntegrationEvent
}

func (p *integrationConsumerProvider) Expand(
	context.Context,
	integrationstore.IntegrationRecord,
	[]byte,
) (IntegrationInboxExpansion, error) {
	p.expansions++
	return IntegrationInboxExpansion{Event: p.event, Files: map[string]IntegrationInboxFile{"F123": p.file}}, nil
}

func (p *integrationConsumerProvider) DownloadFile(
	context.Context,
	integrationstore.IntegrationRecord,
	[]byte,
	string,
) (IntegrationInboxFile, error) {
	p.downloads++
	return p.file, nil
}

func TestIntegrationConsumerRecoveryChecksFrozenContentBeforeUpload(t *testing.T) {
	content := []byte("pinned file")
	file := IntegrationInboxFile{Content: content, ContentType: "text/plain", Filename: "review.txt"}
	id := uuid.Must(uuid.NewV7())
	expected := artifactstore.PreparedArtifact{
		ID:          id,
		ContentType: file.ContentType,
		Filename:    file.Filename,
		Digest:      blobstore.ContentDigest(content),
		SizeBytes:   int64(len(content)),
	}
	recipient := IntegrationInboxRecipient{
		AgentID:     uuid.Must(uuid.NewV7()),
		ArtifactIDs: []uuid.UUID{id},
	}
	message := executionstore.InboxMessage{
		ContentBlocks: []byte(`[{"type":"media_ref","artifact_id":"` + id.String() + `"}]`),
		Files:         []executionstore.InboxPlannedFile{{ArtifactID: id, ProviderFileID: "F123", Expected: &expected}},
	}
	uploads := &integrationConsumerUploads{}
	provider := &integrationConsumerProvider{file: file}
	consumer := NewIntegrationInboxConsumer(nil, nil, uploads, nil, nil, testIntegrationLaunchWorkflow(nil))
	cache := map[string]IntegrationInboxFile{}
	prepared, err := consumer.prepareFiles(
		t.Context(),
		provider,
		integrationstore.IntegrationRecord{},
		nil,
		message,
		recipient,
		cache,
	)
	require.NoError(t, err)
	require.Equal(t, []artifactstore.PreparedArtifact{expected}, prepared)
	require.Equal(t, 1, provider.downloads)
	require.Equal(t, content, uploads.content)
	uploads.present = true
	prepared, err = consumer.prepareFiles(
		t.Context(),
		nil,
		integrationstore.IntegrationRecord{},
		nil,
		message,
		recipient,
		nil,
	)
	require.NoError(t, err)
	require.Equal(t, []artifactstore.PreparedArtifact{expected}, prepared)
	require.Equal(t, 1, uploads.uploads)
	uploads.present = false
	provider.file.Content = []byte("changed upstream")
	_, err = consumer.prepareFiles(
		t.Context(),
		provider,
		integrationstore.IntegrationRecord{},
		nil,
		message,
		recipient,
		map[string]IntegrationInboxFile{},
	)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.Equal(t, 1, uploads.uploads)
}
