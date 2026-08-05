package artifactstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrBlobStoreNotConfigured = errors.New("blob store is not configured")

type ArtifactRecord struct {
	ID             ID        `json:"id"`
	ProjectID      ID        `json:"project_id"`
	AgentID        ID        `json:"agent_id"`
	ContentType    string    `json:"content_type"`
	Filename       string    `json:"filename,omitempty"`
	Digest         string    `json:"digest,omitempty"`
	SizeBytes      *int64    `json:"size_bytes,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Created        bool      `json:"-"`
}

type CreateArtifactInput struct {
	ProjectID      ID
	AgentID        ID
	ContentType    string
	Filename       string
	Digest         string
	SizeBytes      *int64
	Content        []byte
	MaxBytes       int64
	IdempotencyKey string
}

func artifactObjectKey(agentID, artifactID ID) string {
	return "artifacts/" + agentID.String() + "/" + artifactID.String()
}

func (s *Store) CreateArtifact(
	ctx context.Context,
	input CreateArtifactInput,
) (ArtifactRecord, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) {
		return ArtifactRecord{}, errors.New("project id and agent id are required")
	}
	if input.ContentType == "" {
		return ArtifactRecord{}, errors.New("artifact content type is required")
	}
	if len(input.Content) == 0 {
		return ArtifactRecord{}, errors.New("artifact content is required")
	}
	if input.MaxBytes > 0 && int64(len(input.Content)) > input.MaxBytes {
		return ArtifactRecord{}, fmt.Errorf("artifact content exceeds %d bytes", input.MaxBytes)
	}
	if s.blobs == nil {
		return ArtifactRecord{}, ErrBlobStoreNotConfigured
	}
	id, err := uuid.NewV7()
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("generate artifact id: %w", err)
	}
	artifactID := id
	artifactKey := artifactObjectKey(input.AgentID, artifactID)
	metadata, err := s.blobs.PutBlob(ctx, artifactKey, input.Content)
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("upload artifact content: %w", err)
	}
	cleanupUploadedBlob := func(cause error) error {
		if err := s.blobs.DeleteBlob(ctx, artifactKey); err != nil {
			return errors.Join(cause, fmt.Errorf("cleanup uploaded artifact content: %w", err))
		}
		return cause
	}
	input.Digest = metadata.Digest
	input.SizeBytes = &metadata.SizeBytes

	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ArtifactRecord{}, cleanupUploadedBlob(fmt.Errorf("begin create artifact: %w", err))
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, inserted, err := insertArtifactTx(ctx, tx, artifactID, input)
	if err != nil {
		return ArtifactRecord{}, cleanupUploadedBlob(err)
	}
	if !inserted {
		if err := validateArtifactReplay(record, input); err != nil {
			return ArtifactRecord{}, cleanupUploadedBlob(err)
		}
		if err := tx.Commit(ctx); err != nil {
			return ArtifactRecord{}, cleanupUploadedBlob(
				fmt.Errorf("commit idempotent create artifact: %w", err),
			)
		}
		return record, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactRecord{}, cleanupUploadedBlob(fmt.Errorf("commit create artifact: %w", err))
	}
	return record, nil
}

func (s *Store) GetArtifact(
	ctx context.Context,
	projectID, agentID, id ID,
) (ArtifactRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return ArtifactRecord{}, errors.New("project id, agent id, and artifact id are required")
	}
	record, err := loadArtifact(ctx, s.q, projectID, agentID, id)
	if err != nil {
		return ArtifactRecord{}, err
	}
	return record, nil
}

func (s *Store) GetArtifactBlob(
	ctx context.Context,
	projectID, agentID, id ID,
) ([]byte, ArtifactRecord, error) {
	record, err := s.GetArtifact(ctx, projectID, agentID, id)
	if err != nil {
		return nil, ArtifactRecord{}, err
	}
	if s.blobs == nil {
		return nil, ArtifactRecord{}, ErrBlobStoreNotConfigured
	}
	content, _, err := s.blobs.GetBlob(ctx, artifactObjectKey(record.AgentID, record.ID))
	if err != nil {
		if errors.Is(err, blobstore.ErrNotFound) {
			return nil, ArtifactRecord{}, fmt.Errorf(
				"load artifact %s content: %w",
				record.ID,
				storeerr.ErrNotFound,
			)
		}
		return nil, ArtifactRecord{}, fmt.Errorf("load artifact %s content: %w", record.ID, err)
	}
	return content, record, nil
}

func (s *Store) ListAgentArtifactsByIDs(
	ctx context.Context,
	projectID, agentID ID,
	ids []ID,
) ([]ArtifactRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project id and agent id are required")
	}
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.q.ListArtifactsByIDs(
		ctx,
		dbsqlc.ListArtifactsByIDsParams{ProjectID: projectID, AgentID: agentID, Ids: ids},
	)
	if err != nil {
		return nil, fmt.Errorf("list artifacts: %w", err)
	}
	records := make([]ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		records = append(records, artifactRecordFromListSQLC(row))
	}
	return records, nil
}

func insertArtifactTx(
	ctx context.Context,
	tx pgx.Tx,
	artifactID ID,
	input CreateArtifactInput,
) (ArtifactRecord, bool, error) {
	row, err := dbsqlc.New(tx).InsertArtifact(ctx, dbsqlc.InsertArtifactParams{
		ID:             artifactID,
		ProjectID:      input.ProjectID,
		AgentID:        input.AgentID,
		ContentType:    input.ContentType,
		Filename:       sqlcTextFromEmpty(input.Filename),
		Digest:         sqlcTextFromEmpty(input.Digest),
		SizeBytes:      input.SizeBytes,
		IdempotencyKey: sqlcTextFromEmpty(input.IdempotencyKey),
	})
	if err == nil {
		record := artifactRecordFromInsertSQLC(row)
		record.Created = true
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		if storeutil.IsUniqueViolation(err) {
			return ArtifactRecord{}, false, storeerr.ErrIdempotencyConflict
		}
		return ArtifactRecord{}, false, fmt.Errorf("insert artifact: %w", err)
	}
	if input.IdempotencyKey == "" {
		return ArtifactRecord{}, false, fmt.Errorf("insert artifact: %w", err)
	}
	record, err := loadArtifactForReplayTx(
		ctx,
		tx,
		input.ProjectID,
		input.AgentID,
		input.IdempotencyKey,
	)
	return record, false, err
}

func loadArtifact(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, id ID,
) (ArtifactRecord, error) {
	row, err := q.GetArtifact(
		ctx,
		dbsqlc.GetArtifactParams{ProjectID: projectID, AgentID: agentID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ArtifactRecord{}, storeerr.ErrNotFound
		}
		return ArtifactRecord{}, fmt.Errorf("get artifact: %w", err)
	}
	return artifactRecordFromGetSQLC(row), nil
}

func loadArtifactForReplayTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	idempotencyKey string,
) (ArtifactRecord, error) {
	row, err := dbsqlc.New(tx).
		GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
			ProjectID:      projectID,
			AgentID:        agentID,
			IdempotencyKey: idempotencyKey,
		})
	if err != nil {
		return ArtifactRecord{}, err
	}
	return artifactRecordFromIdempotencySQLC(row), nil
}

func validateArtifactReplay(record ArtifactRecord, input CreateArtifactInput) error {
	if record.ProjectID == input.ProjectID &&
		record.AgentID == input.AgentID &&
		record.Digest == input.Digest &&
		record.ContentType == input.ContentType &&
		record.Filename == input.Filename {
		return nil
	}
	return storeerr.ErrIdempotencyConflict
}
