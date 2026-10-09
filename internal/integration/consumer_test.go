package integration

import (
	"context"
	"errors"
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
	present   bool
	probed    []uuid.UUID
	uploads   int
	content   []byte
	probeErr  error
	uploadErr error
}

func (s *integrationConsumerUploads) PreparedArtifactUploaded(
	_ context.Context,
	_ uuid.UUID,
	prepared artifactstore.PreparedArtifact,
) (bool, error) {
	s.probed = append(s.probed, prepared.ID)
	return s.present, s.probeErr
}

func (s *integrationConsumerUploads) UploadPreparedArtifact(
	_ context.Context,
	_ uuid.UUID,
	_ artifactstore.PreparedArtifact,
	content []byte,
) error {
	s.uploads++
	s.content = append([]byte(nil), content...)
	return s.uploadErr
}

type integrationConsumerProvider struct {
	IntegrationInboxProvider
	downloads   int
	file        IntegrationInboxFile
	expansions  int
	event       *IntegrationEvent
	downloadErr error
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
	return p.file, p.downloadErr
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

func TestIntegrationConsumerPresenceProbeFailures(t *testing.T) {
	probeFailure := errors.New("S3 GetObject temporarily unavailable")
	uploadFailure := errors.New("S3 PutObject denied")
	downloadFailure := errors.New("provider file no longer available")
	for _, tc := range []struct {
		name                                      string
		probeErr, uploadErr, downloadErr, wantErr error
		wantDownloads, wantUploads                int
	}{
		{name: "known conflict stops before downloading", probeErr: storeerr.ErrIdempotencyConflict,
			wantErr: storeerr.ErrIdempotencyConflict},
		{name: "transient probe failure permits conditional create", probeErr: probeFailure,
			wantDownloads: 1, wantUploads: 1},
		{name: "failed create preserves probe diagnostics", probeErr: probeFailure, uploadErr: uploadFailure,
			wantErr: uploadFailure, wantDownloads: 1, wantUploads: 1},
		{name: "failed download preserves probe diagnostics", probeErr: probeFailure, downloadErr: downloadFailure,
			wantErr: downloadFailure, wantDownloads: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			file := IntegrationInboxFile{Content: []byte("pinned bytes"), ContentType: "text/plain", Filename: "review.txt"}
			expected := artifactstore.PreparedArtifact{
				ID: uuid.Must(uuid.NewV7()), ContentType: file.ContentType, Filename: file.Filename,
				Digest: blobstore.ContentDigest(file.Content), SizeBytes: int64(len(file.Content)),
			}
			message := executionstore.InboxMessage{
				ContentBlocks: []byte(`[{"type":"media_ref","artifact_id":"` + expected.ID.String() + `"}]`),
				Files: []executionstore.InboxPlannedFile{{
					ArtifactID: expected.ID, ProviderFileID: "F123", Expected: &expected,
				}},
			}
			uploads := &integrationConsumerUploads{probeErr: tc.probeErr, uploadErr: tc.uploadErr}
			provider := &integrationConsumerProvider{file: file, downloadErr: tc.downloadErr}
			consumer := NewIntegrationInboxConsumer(nil, nil, uploads, nil, nil, nil)
			prepared, err := consumer.prepareFiles(t.Context(), provider, integrationstore.IntegrationRecord{}, nil,
				message, IntegrationInboxRecipient{
					AgentID: uuid.Must(uuid.NewV7()), ArtifactIDs: []uuid.UUID{expected.ID},
				}, nil)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
				require.ErrorIs(t, err, tc.probeErr)
				require.Empty(t, prepared)
			} else {
				require.NoError(t, err)
				require.Equal(t, []artifactstore.PreparedArtifact{expected}, prepared)
			}
			require.Equal(t, tc.wantDownloads, provider.downloads)
			require.Equal(t, tc.wantUploads, uploads.uploads)
		})
	}
}
