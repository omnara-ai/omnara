package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

// Reuse inline-media validation and decoded buffers. Upload for the provisional
// agent before opening the admission tx; record persistence belongs to delivery.
func (s *Server) prepareChannelInputMedia(
	ctx context.Context,
	ingest mediaIngestContext,
	plan inlineMediaPlan,
) (executionstore.PreparedInputContent, error) {
	content := executionstore.PreparedInputContent{}
	if !plan.validated {
		return content, errors.New("inline media plan was not preflighted")
	}
	blocks := append([]json.RawMessage(nil), plan.rawBlocks...)
	var uploads []*artifactstore.PreparedArtifact
	for ordinal := range blocks {
		attachment, ok := plan.attachments[ordinal]
		if !ok {
			continue
		}
		prepared, err := s.store.Artifacts().PrepareArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID: ingest.ProjectID, AgentID: ingest.AgentID,
			ContentType: attachment.mediaType, Filename: attachment.filename, Content: attachment.content,
			MaxBytes: maxAttachmentBytes, IdempotencyKey: fmt.Sprintf("upload:%s:%d", ingest.IdempotencyKey, ordinal),
		})
		if err != nil {
			cleanupErr := s.store.Artifacts().FinishPreparedArtifacts(
				ctx, artifactstore.ArtifactTransactionRolledBack, uploads...)
			return executionstore.PreparedInputContent{}, errors.Join(
				fmt.Errorf("prepare attachment %d: %w", ordinal, err), cleanupErr)
		}
		uploads = append(uploads, prepared)
		content.Attachments = append(content.Attachments, executionstore.PreparedInputAttachment{
			Ordinal: ordinal, Artifact: prepared, Metadata: attachment.metadata,
		})
		// The owning transaction replaces this slot with its canonical artifact
		// reference. Do not carry a second copy of base64 bytes into that tx.
		blocks[ordinal] = json.RawMessage(`{"type":"text","text":""}`)
	}
	raw, err := json.Marshal(blocks)
	if err != nil {
		cleanupErr := s.store.Artifacts().FinishPreparedArtifacts(
			ctx, artifactstore.ArtifactTransactionRolledBack, uploads...)
		return executionstore.PreparedInputContent{}, errors.Join(err, cleanupErr)
	}
	content.Blocks = raw
	return content, nil
}
