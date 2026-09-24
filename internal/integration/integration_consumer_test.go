package integration

import (
	"context"
	"testing"

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
	events     []IntegrationEvent
}

func (p *integrationConsumerProvider) Expand(
	context.Context,
	integrationstore.ProjectIntegrationRecord,
	[]byte,
) (IntegrationInboxExpansion, error) {
	p.expansions++
	return IntegrationInboxExpansion{Events: p.events, Files: map[string]IntegrationInboxFile{"F123": p.file}}, nil
}

func (p *integrationConsumerProvider) DownloadFile(
	context.Context,
	integrationstore.ProjectIntegrationRecord,
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
	slot := IntegrationInboxSlot{
		AgentID: uuid.Must(uuid.NewV7()),
		Files:   []IntegrationPlannedFile{{ArtifactID: id, ProviderFileID: "F123", Expected: &expected}},
	}
	uploads := &integrationConsumerUploads{}
	provider := &integrationConsumerProvider{file: file}
	consumer := NewIntegrationInboxConsumer(nil, nil, uploads, nil, nil, testIntegrationLaunchWorkflow(nil))
	cache := map[string]IntegrationInboxFile{}
	prepared, err := consumer.prepareFiles(
		t.Context(),
		provider,
		integrationstore.ProjectIntegrationRecord{},
		nil,
		slot,
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
		integrationstore.ProjectIntegrationRecord{},
		nil,
		slot,
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
		integrationstore.ProjectIntegrationRecord{},
		nil,
		slot,
		map[string]IntegrationInboxFile{},
	)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.Equal(t, 1, uploads.uploads)
}
