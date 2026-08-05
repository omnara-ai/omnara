package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type integrationFileIngestResult struct {
	Blocks []map[string]any
	Files  []slack.EventFileResult
}

func (s *Server) ingestIntegrationEventFiles(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
	integrationTarget integrationstore.IntegrationTargetRecord,
	files []slack.File,
	token string,
) (integrationFileIngestResult, error) {
	if len(files) == 0 {
		return integrationFileIngestResult{}, nil
	}
	downloaded, err := slack.DownloadEventFiles(
		ctx,
		s.slackOAuth,
		token,
		files,
		slack.FileDownloadOptions{
			MaxFiles:         maxAttachmentsPerInput,
			MaxFileBytes:     maxAttachmentBytes,
			MaxTotalBytes:    maxTotalAttachmentBytes,
			MaxFilenameBytes: maxAttachmentFilenameBytes,
			AcceptMediaType:  modelcontext.IsAttachmentMedia,
			DefaultFilename:  modelcontext.MediaFilename,
		},
	)
	if err != nil {
		return integrationFileIngestResult{}, err
	}
	result := integrationFileIngestResult{
		Files: downloaded,
	}
	for i := range result.Files {
		file := &result.Files[i]
		if len(file.Content) == 0 || file.ContentType == "" {
			continue
		}
		block, err := s.storeIntegrationEventFile(ctx, install, integrationTarget, *file)
		if err != nil {
			return integrationFileIngestResult{}, err
		}
		result.Blocks = append(result.Blocks, block)
		file.Status = slack.EventFileStatusStored
	}
	return result, nil
}

func (s *Server) storeIntegrationEventFile(
	ctx context.Context,
	install integrationstore.IntegrationInstallRecord,
	integrationTarget integrationstore.IntegrationTargetRecord,
	file slack.EventFileResult,
) (map[string]any, error) {
	artifact, err := s.store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID:   install.ProjectID,
		AgentID:     integrationTarget.AgentID,
		ContentType: file.ContentType,
		Filename:    file.Filename,
		Content:     file.Content,
		MaxBytes:    maxAttachmentBytes,
		IdempotencyKey: integrationArtifactIdempotencyKey(
			install.Provider,
			file.Content,
			file.ContentType,
			file.Filename,
		),
	})
	if err != nil {
		return nil, fmt.Errorf("store integration file artifact: %w", err)
	}
	block := map[string]any{
		"type":        "media_ref",
		"artifact_id": artifact.ID.String(),
	}
	return block, nil
}

func integrationArtifactIdempotencyKey(provider string, content []byte, contentType, filename string) string {
	digest := blobstore.ContentDigest(content)
	sum := sha256.Sum256([]byte(digest + "\x00" + contentType + "\x00" + filename))
	return provider + ":artifact:" + hex.EncodeToString(sum[:])
}
