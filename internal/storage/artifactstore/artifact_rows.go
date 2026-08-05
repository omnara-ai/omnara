package artifactstore

import (
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func artifactRecordFromInsertSQLC(row dbsqlc.InsertArtifactRow) ArtifactRecord {
	return artifactRecordFromSQLC(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.ContentType,
		row.Filename,
		row.Digest,
		row.SizeBytes,
		row.IdempotencyKey,
		row.CreatedAt,
	)
}

func artifactRecordFromGetSQLC(row dbsqlc.GetArtifactRow) ArtifactRecord {
	return artifactRecordFromSQLC(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.ContentType,
		row.Filename,
		row.Digest,
		row.SizeBytes,
		row.IdempotencyKey,
		row.CreatedAt,
	)
}

func artifactRecordFromIdempotencySQLC(row dbsqlc.GetArtifactByIdempotencyKeyRow) ArtifactRecord {
	return artifactRecordFromSQLC(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.ContentType,
		row.Filename,
		row.Digest,
		row.SizeBytes,
		row.IdempotencyKey,
		row.CreatedAt,
	)
}

func artifactRecordFromListSQLC(row dbsqlc.ListArtifactsByIDsRow) ArtifactRecord {
	return artifactRecordFromSQLC(
		row.ID,
		row.ProjectID,
		row.AgentID,
		row.ContentType,
		row.Filename,
		row.Digest,
		row.SizeBytes,
		row.IdempotencyKey,
		row.CreatedAt,
	)
}

func artifactRecordFromSQLC(
	id ID,
	projectID ID,
	agentID ID,
	contentType string,
	filename string,
	digest string,
	sizeBytes *int64,
	idempotencyKey string,
	createdAt time.Time,
) ArtifactRecord {
	return ArtifactRecord{
		ID:             id,
		ProjectID:      projectID,
		AgentID:        agentID,
		ContentType:    contentType,
		Filename:       filename,
		Digest:         digest,
		SizeBytes:      sizeBytes,
		IdempotencyKey: idempotencyKey,
		CreatedAt:      createdAt,
	}
}
