package integrationstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) lockConnectorIntegrationAuthority(
	ctx context.Context,
	tx pgx.Tx,
	projectID, installID ID,
	capabilities []channelconnector.Capability,
) (dbsqlc.LockConnectorIntegrationAuthorityRow, error) {
	keys, providers, err := normalizedCapabilityColumns(capabilities)
	if err != nil {
		return dbsqlc.LockConnectorIntegrationAuthorityRow{}, err
	}
	if _, err := lockIntegrationInstallLifecycleShared(ctx, tx, projectID, installID); err != nil {
		return dbsqlc.LockConnectorIntegrationAuthorityRow{}, err
	}
	row, err := s.q.WithTx(tx).LockConnectorIntegrationAuthority(ctx, dbsqlc.LockConnectorIntegrationAuthorityParams{
		ProjectID: projectID, IntegrationInstallID: installID, ConnectorKeys: keys, Providers: providers,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return row, storeerr.ErrNotFound
	}
	return row, err
}

// LockConnectorInstallationTx holds the live project/install/app authority until
// the caller commits its product mutation. It does not expose query types.
func (s *Store) LockConnectorInstallationTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, installID ID,
	capabilities []channelconnector.Capability,
) (IntegrationInstallRecord, error) {
	if tx == nil {
		return IntegrationInstallRecord{}, errors.New("transaction is required")
	}
	if _, err := s.lockConnectorIntegrationAuthority(ctx, tx, projectID, installID, capabilities); err != nil {
		return IntegrationInstallRecord{}, err
	}
	return s.GetIntegrationInstallByIDTx(ctx, tx, installID)
}
