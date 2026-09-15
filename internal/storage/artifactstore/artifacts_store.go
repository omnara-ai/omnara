package artifactstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var ErrBlobStoreNotConfigured = errors.New("blob store is not configured")

const artifactCompensationTimeout = 10 * time.Second

type ArtifactRecord struct {
	ID             uuid.UUID `json:"id"`
	ProjectID      uuid.UUID `json:"project_id"`
	AgentID        uuid.UUID `json:"agent_id"`
	ContentType    string    `json:"content_type"`
	Filename       string    `json:"filename,omitempty"`
	Digest         string    `json:"digest,omitempty"`
	SizeBytes      *int64    `json:"size_bytes,omitempty"`
	IdempotencyKey string    `json:"idempotency_key,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Created        bool      `json:"-"`
}

type CreateArtifactInput struct {
	ProjectID      uuid.UUID
	AgentID        uuid.UUID
	ContentType    string
	Filename       string
	Digest         string
	SizeBytes      *int64
	Content        []byte
	MaxBytes       int64
	IdempotencyKey string

	integrationInstallID uuid.UUID
	runtimeLease         *integrationstore.IntegrationRuntimeLeaseProof
}

func artifactObjectKey(agentID, artifactID uuid.UUID) string {
	return "artifacts/" + agentID.String() + "/" + artifactID.String()
}

func (s *Store) CreateArtifact(
	ctx context.Context,
	input CreateArtifactInput,
) (ArtifactRecord, error) {
	prepared, err := s.PrepareArtifact(ctx, input)
	if err != nil {
		return ArtifactRecord{}, err
	}
	record, outcome, err := s.createArtifactRecord(ctx, prepared)
	// The owned transaction settles before any external compensation starts.
	cleanupErr := s.FinishPreparedArtifacts(ctx, outcome, prepared)
	if err != nil && cleanupErr != nil {
		err = errors.Join(err, fmt.Errorf("cleanup uploaded artifact content: %w", cleanupErr))
	} else if cleanupErr != nil {
		event := log.NewEvent(ctx, "artifact.replay.cleanup", log.Fields{
			"project.id": input.ProjectID, "agent.id": input.AgentID,
			"artifact.id": record.ID, "blob.key": prepared.key,
		})
		event.Level(log.WarnLevel)
		event.Error(cleanupErr)
		event.Done(ctx)
	}
	return record, err
}

func (s *Store) createArtifactRecord(
	ctx context.Context,
	prepared *PreparedArtifact,
) (ArtifactRecord, ArtifactTransactionOutcome, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ArtifactRecord{}, ArtifactTransactionRolledBack, fmt.Errorf("begin create artifact: %w", err)
	}
	defer func() {
		rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactCompensationTimeout)
		defer cancel()
		_ = tx.Rollback(rollbackCtx)
	}()
	// Standalone creation historically permits identical replay after archival.
	// Composed writes require an active agent, including when an artifact replays.
	record, err := s.persistPreparedArtifact(ctx, tx, prepared, false)
	if err != nil {
		return ArtifactRecord{}, ArtifactTransactionRolledBack, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ArtifactRecord{}, ArtifactTransactionUnknown, fmt.Errorf("commit create artifact: %w", err)
	}
	return record, ArtifactTransactionCommitted, nil
}

func persistArtifactRecordTx(
	ctx context.Context,
	tx pgx.Tx,
	artifactID uuid.UUID,
	input CreateArtifactInput,
	requireActiveAgent bool,
) (ArtifactRecord, error) {
	qtx := dbsqlc.New(tx)
	if input.runtimeLease != nil {
		install, err := qtx.GetIntegrationInstall(ctx, dbsqlc.GetIntegrationInstallParams{
			ProjectID: input.ProjectID, ID: input.integrationInstallID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			return ArtifactRecord{}, storeerr.ErrNotFound
		}
		if err != nil {
			return ArtifactRecord{}, fmt.Errorf("load runtime artifact installation: %w", err)
		}
		if err := lifecyclelock.EnterActiveProject(ctx, tx, install.OrgID, input.ProjectID); err != nil {
			return ArtifactRecord{}, err
		}
		if err := qtx.LockIntegrationInstallLifecycleShared(ctx, dbsqlc.LockIntegrationInstallLifecycleSharedParams{
			InstallID: input.integrationInstallID,
		}); err != nil {
			return ArtifactRecord{}, fmt.Errorf("lock runtime artifact installation lifecycle: %w", err)
		}
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
	}}); err != nil {
		return ArtifactRecord{}, err
	}
	if err := integrationstore.LockIntegrationRuntimeLeaseForMutation(
		ctx,
		qtx,
		input.runtimeLease,
		input.ProjectID,
		input.integrationInstallID,
	); err != nil {
		return ArtifactRecord{}, err
	}
	agent, err := qtx.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{
		ProjectID: input.ProjectID,
		ID:        input.AgentID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return ArtifactRecord{}, fmt.Errorf("revalidate artifact agent: %w", err)
	}
	if requireActiveAgent && agent.State != "active" {
		return ArtifactRecord{}, storeerr.ErrStateTransitionConflict
	}
	if input.IdempotencyKey != "" {
		replay, found, err := findArtifactReplayTx(ctx, qtx, input)
		if err != nil {
			return ArtifactRecord{}, err
		}
		if found {
			return replay, nil
		}
	}
	if agent.State != "active" {
		return ArtifactRecord{}, storeerr.ErrStateTransitionConflict
	}
	record, err := insertArtifactTx(ctx, tx, artifactID, input)
	if err != nil {
		return ArtifactRecord{}, err
	}
	return record, nil
}

func (s *Store) deleteProvisionalArtifactBlob(ctx context.Context, key string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), artifactCompensationTimeout)
	defer cancel()
	return s.blobs.DeleteBlob(cleanupCtx, key)
}

// CreateArtifactWithIntegrationRuntimeLease fences the artifact row against
// runtime ownership in the same transaction. If ownership is already stale,
// the provisional blob is removed by CreateArtifact's normal cleanup path.
func (s *Store) CreateArtifactWithIntegrationRuntimeLease(
	ctx context.Context,
	input CreateArtifactInput,
	integrationInstallID uuid.UUID,
	proof *integrationstore.IntegrationRuntimeLeaseProof,
) (ArtifactRecord, error) {
	if proof == nil {
		return ArtifactRecord{}, errors.New("runtime lease proof is required")
	}
	input.integrationInstallID = integrationInstallID
	input.runtimeLease = proof
	return s.CreateArtifact(ctx, input)
}

func findArtifactReplayTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input CreateArtifactInput,
) (ArtifactRecord, bool, error) {
	row, err := qtx.GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
		ProjectID:      input.ProjectID,
		AgentID:        input.AgentID,
		IdempotencyKey: input.IdempotencyKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return ArtifactRecord{}, false, nil
	}
	if err != nil {
		return ArtifactRecord{}, false, fmt.Errorf("load artifact replay: %w", err)
	}
	record := artifactRecordFromIdempotencySQLC(row)
	if err := validateArtifactReplay(record, input); err != nil {
		return ArtifactRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) GetArtifact(
	ctx context.Context,
	projectID, agentID, id uuid.UUID,
) (ArtifactRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || id == uuid.Nil {
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
	projectID, agentID, id uuid.UUID,
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
	projectID, agentID uuid.UUID,
	ids []uuid.UUID,
) ([]ArtifactRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
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
	artifactID uuid.UUID,
	input CreateArtifactInput,
) (ArtifactRecord, error) {
	row, err := dbsqlc.New(tx).InsertArtifact(ctx, dbsqlc.InsertArtifactParams{
		ID:             artifactID,
		ProjectID:      input.ProjectID,
		AgentID:        input.AgentID,
		ContentType:    input.ContentType,
		Filename:       storeutil.TextFromEmpty(input.Filename),
		Digest:         storeutil.TextFromEmpty(input.Digest),
		SizeBytes:      input.SizeBytes,
		IdempotencyKey: storeutil.TextFromEmpty(input.IdempotencyKey),
	})
	if err != nil {
		if storeutil.IsUniqueViolation(err) {
			return ArtifactRecord{}, storeerr.ErrIdempotencyConflict
		}
		return ArtifactRecord{}, fmt.Errorf("insert artifact: %w", err)
	}
	record := artifactRecordFromInsertSQLC(row)
	record.Created = true
	return record, nil
}

func loadArtifact(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, id uuid.UUID,
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

func validateArtifactReplay(record ArtifactRecord, input CreateArtifactInput) error {
	if record.ProjectID == input.ProjectID &&
		record.AgentID == input.AgentID &&
		record.Digest == input.Digest &&
		record.ContentType == input.ContentType &&
		record.Filename == input.Filename &&
		(record.SizeBytes == nil || input.SizeBytes == nil || *record.SizeBytes == *input.SizeBytes) {
		return nil
	}
	return storeerr.ErrIdempotencyConflict
}
