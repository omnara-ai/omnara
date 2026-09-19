package integrationstore

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func newIntegrationTargetRef(provider string) (string, error) {
	var buf [4]byte
	if _, err := io.ReadFull(rand.Reader, buf[:]); err != nil {
		return "", fmt.Errorf("generate integration target ref: %w", err)
	}
	return fmt.Sprintf("%s-%c%c%c%c", provider,
		integrationTargetRefAlphabet[int(buf[0])%len(integrationTargetRefAlphabet)],
		integrationTargetRefAlphabet[int(buf[1])%len(integrationTargetRefAlphabet)],
		integrationTargetRefAlphabet[int(buf[2])%len(integrationTargetRefAlphabet)],
		integrationTargetRefAlphabet[int(buf[3])%len(integrationTargetRefAlphabet)]), nil
}

const integrationTargetRefAlphabet = "abcdefghijklmnpqrstvwxyz23456789"

func (s *Store) UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
	ctx context.Context,
	projectID, connectionID uuid.UUID,
	providerRefPrefix, displayName string,
) error {
	if projectID == uuid.Nil || connectionID == uuid.Nil || providerRefPrefix == "" || displayName == "" {
		return errors.New("project, integration connection, provider ref prefix, and display name are required")
	}
	_, err := s.q.UpdateIntegrationTargetDisplayNamesByProviderRefPrefix(
		ctx,
		dbsqlc.UpdateIntegrationTargetDisplayNamesByProviderRefPrefixParams{
			ProjectID:               projectID,
			IntegrationConnectionID: connectionID,
			ProviderRefPrefix:       providerRefPrefix,
			DisplayName:             displayName,
		},
	)
	if err != nil {
		return fmt.Errorf("update integration target display names: %w", err)
	}
	return nil
}

func (s *Store) GetIntegrationTarget(
	ctx context.Context,
	projectID, id uuid.UUID,
) (IntegrationTargetRecord, error) {
	return getIntegrationTarget(ctx, s.q, projectID, id)
}

func (s *Store) GetIntegrationTargetTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, id uuid.UUID,
) (IntegrationTargetRecord, error) {
	return getIntegrationTarget(ctx, dbsqlc.New(tx), projectID, id)
}

func getIntegrationTarget(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, id uuid.UUID,
) (IntegrationTargetRecord, error) {
	row, err := q.GetIntegrationTarget(
		ctx,
		dbsqlc.GetIntegrationTargetParams{ProjectID: projectID, ID: id},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return IntegrationTargetRecord{}, storeerr.ErrNotFound
		}
		return IntegrationTargetRecord{}, fmt.Errorf("get integration target: %w", err)
	}
	return integrationTargetRecordFromGetSQLC(row), nil
}

func integrationTargetRecordFromGetSQLC(
	row dbsqlc.GetIntegrationTargetRow,
) IntegrationTargetRecord {
	return integrationTargetRecordFromFields(
		row.ID, row.OrgID, row.ProjectID, row.AgentID, row.IntegrationConnectionID,
		row.TargetRef, row.ProviderRef, row.ProviderRefKind, row.DisplayName,
		row.ProviderMetadata, row.CreatedAt, row.UpdatedAt,
	)
}

func integrationTargetRecordFromFields(
	id, orgID, projectID, agentID, integrationConnectionID uuid.UUID,
	targetRef, providerRef, providerRefKind, displayName string,
	providerMetadata json.RawMessage,
	createdAt, updatedAt time.Time,
) IntegrationTargetRecord {
	return IntegrationTargetRecord{
		ID:                      id,
		OrgID:                   orgID,
		ProjectID:               projectID,
		AgentID:                 agentID,
		IntegrationConnectionID: integrationConnectionID,
		TargetRef:               targetRef,
		ProviderRef:             providerRef,
		ProviderRefKind:         providerRefKind,
		DisplayName:             displayName,
		ProviderMetadata:        providerMetadata,
		CreatedAt:               createdAt,
		UpdatedAt:               updatedAt,
	}
}
