package apps

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type appConsumerUploads struct {
	present bool
	uploads int
	content []byte
}

func (s *appConsumerUploads) PreparedArtifactUploaded(
	context.Context,
	uuid.UUID,
	artifactstore.PreparedArtifact,
) (bool, error) {
	return s.present, nil
}

func (s *appConsumerUploads) UploadPreparedArtifact(
	_ context.Context,
	_ uuid.UUID,
	_ artifactstore.PreparedArtifact,
	content []byte,
) error {
	s.uploads++
	s.content = append([]byte(nil), content...)
	return nil
}

type appConsumerProvider struct {
	AppInboxProvider
	downloads  int
	file       AppInboxFile
	expansions int
	events     []AppEvent
}

func (p *appConsumerProvider) Expand(
	context.Context,
	appstore.ProjectAppRecord,
	[]byte,
) (AppInboxExpansion, error) {
	p.expansions++
	return AppInboxExpansion{Events: p.events, Files: map[string]AppInboxFile{"F123": p.file}}, nil
}

func (p *appConsumerProvider) DownloadFile(
	context.Context,
	appstore.ProjectAppRecord,
	[]byte,
	string,
) (AppInboxFile, error) {
	p.downloads++
	return p.file, nil
}

func TestAppConsumerRecoveryChecksFrozenContentBeforeUpload(t *testing.T) {
	content := []byte("pinned file")
	file := AppInboxFile{Content: content, ContentType: "text/plain", Filename: "review.txt"}
	id := uuid.Must(uuid.NewV7())
	expected := artifactstore.PreparedArtifact{
		ID:          id,
		ContentType: file.ContentType,
		Filename:    file.Filename,
		Digest:      blobstore.ContentDigest(content),
		SizeBytes:   int64(len(content)),
	}
	slot := AppInboxSlot{
		AgentID: uuid.Must(uuid.NewV7()),
		Files:   []AppPlannedFile{{ArtifactID: id, ProviderFileID: "F123", Expected: &expected}},
	}
	uploads := &appConsumerUploads{}
	provider := &appConsumerProvider{file: file}
	consumer := NewAppInboxConsumer(nil, nil, uploads, nil, nil, testAppLaunchWorkflow(nil))
	cache := map[string]AppInboxFile{}
	prepared, err := consumer.prepareFiles(
		t.Context(),
		provider,
		appstore.ProjectAppRecord{},
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
		appstore.ProjectAppRecord{},
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
		appstore.ProjectAppRecord{},
		nil,
		slot,
		map[string]AppInboxFile{},
	)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	require.Equal(t, 1, uploads.uploads)
}
