package artifactstore

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PreparedArtifact struct {
	ID          uuid.UUID `json:"id"`
	ContentType string    `json:"content_type"`
	Filename    string    `json:"filename,omitempty"`
	Digest      string    `json:"digest"`
	SizeBytes   int64     `json:"size_bytes"`
}

func (p PreparedArtifact) Validate() error {
	if p.ID == uuid.Nil || p.ContentType == "" || p.SizeBytes <= 0 {
		return storeerr.InvalidRequest(errors.New("prepared artifact requires an ID, content type and positive size"))
	}
	if err := dbsafe.Text(p.ContentType); err != nil {
		return storeerr.InvalidRequest(fmt.Errorf("artifact content type: %w", err))
	}
	if err := dbsafe.Text(p.Filename); err != nil {
		return storeerr.InvalidRequest(fmt.Errorf("artifact filename: %w", err))
	}
	digest, found := strings.CutPrefix(p.Digest, "sha256:")
	if !found || len(digest) != 64 || strings.ToLower(digest) != digest {
		return storeerr.InvalidRequest(errors.New("prepared artifact requires a canonical SHA-256 digest"))
	}
	if _, err := hex.DecodeString(digest); err != nil {
		return storeerr.InvalidRequest(errors.New("prepared artifact requires a canonical SHA-256 digest"))
	}
	return nil
}

func InsertPreparedArtifactsTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID uuid.UUID,
	artifacts []PreparedArtifact,
) ([]ArtifactRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return nil, storeerr.InvalidRequest(errors.New("project and agent are required"))
	}
	seen := make(map[uuid.UUID]bool, len(artifacts))
	for _, artifact := range artifacts {
		if err := artifact.Validate(); err != nil {
			return nil, err
		}
		if seen[artifact.ID] {
			return nil, storeerr.InvalidRequest(errors.New("duplicate prepared artifact ID"))
		}
		seen[artifact.ID] = true
	}
	if len(artifacts) == 0 {
		return nil, nil
	}
	q := dbsqlc.New(tx)
	if _, err := q.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, storeerr.ErrNotFound
		}
		return nil, fmt.Errorf("lock prepared artifact agent: %w", err)
	}
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: agentID})
	if err != nil {
		return nil, err
	}
	if agent.State != "active" {
		return nil, storeerr.ErrStateTransitionConflict
	}
	records := make([]ArtifactRecord, 0, len(artifacts))
	for _, artifact := range artifacts {
		record, err := loadArtifact(ctx, q, projectID, agentID, artifact.ID)
		if err == nil {
			if record.ContentType != artifact.ContentType || record.Filename != artifact.Filename ||
				record.Digest != artifact.Digest || record.SizeBytes == nil || *record.SizeBytes != artifact.SizeBytes {
				return nil, storeerr.ErrIdempotencyConflict
			}
		} else if errors.Is(err, storeerr.ErrNotFound) {
			record, err = insertArtifactTx(ctx, tx, artifact.ID, CreateArtifactInput{
				ProjectID: projectID, AgentID: agentID, ContentType: artifact.ContentType,
				Filename: artifact.Filename, Digest: artifact.Digest, SizeBytes: &artifact.SizeBytes,
			})
		}
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}
