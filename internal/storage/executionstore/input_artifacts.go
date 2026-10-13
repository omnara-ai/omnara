package executionstore

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func matchPreparedInputContent(
	ctx context.Context, q *dbsqlc.Queries, input CreateAgentContentInputInput,
	blocks []CreateContentBlockInput, existing json.RawMessage,
) (json.RawMessage, error) {
	if len(input.PreparedArtifacts) == 0 {
		return input.ContentBlocks, nil
	}
	stored, err := parseAgentInputContentBlocks(existing)
	if err != nil {
		return nil, err
	}
	if len(stored) != len(blocks) {
		return nil, storeerr.ErrIdempotencyConflict
	}
	prepared := make(map[uuid.UUID]artifactstore.PreparedArtifact, len(input.PreparedArtifacts))
	for _, artifact := range input.PreparedArtifacts {
		prepared[artifact.ID] = artifact
	}
	blocks = slices.Clone(blocks)
	for i, block := range blocks {
		artifact, ok := prepared[block.ArtifactID]
		if !ok {
			continue
		}
		if stored[i].ArtifactID == uuid.Nil {
			return nil, storeerr.ErrIdempotencyConflict
		}
		row, err := q.GetArtifact(ctx, dbsqlc.GetArtifactParams{ProjectID: input.ProjectID,
			AgentID: input.AgentID, ID: stored[i].ArtifactID})
		if err != nil {
			return nil, err
		}
		if row.ContentType != artifact.ContentType || row.Filename != artifact.Filename || row.Digest != artifact.Digest ||
			row.SizeBytes == nil || *row.SizeBytes != artifact.SizeBytes {
			return nil, storeerr.ErrIdempotencyConflict
		}
		blocks[i].ArtifactID = row.ID
	}
	return marshalAgentInputContentBlocks(blocks)
}
