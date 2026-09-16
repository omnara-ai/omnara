package integrationstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// ReceiveIntegrationControl saves verified app-owned work before acknowledgment.
// A replay retains its original scan bound even when configuration has changed.
func (s *Store) ReceiveIntegrationControl(
	ctx context.Context, input ReceiveIntegrationControlInput,
) (IntegrationControlReceipt, error) {
	if input.IntegrationAppID == uuid.Nil {
		return IntegrationControlReceipt{}, storeerr.InvalidRequest(errors.New("app is required"))
	}
	for _, identity := range []string{input.ProviderTenantID, input.EventID} {
		if err := validateProviderControlIdentity(identity); err != nil {
			return IntegrationControlReceipt{}, err
		}
	}
	payload, err := normalizeIntegrationControlObject(input.Payload, MaxIntegrationControlPayloadBytes)
	if err != nil {
		return IntegrationControlReceipt{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return IntegrationControlReceipt{}, fmt.Errorf("begin integration control receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	app, err := s.lockIntegrationControlOwner(ctx, tx, input.IntegrationAppID, input.Capabilities)
	if err != nil {
		return IntegrationControlReceipt{}, err
	}
	q := s.q.WithTx(tx)
	endID, err := connectorInstallationControlScopeEnd(ctx, q, app, input.ProviderTenantID)
	if err != nil {
		return IntegrationControlReceipt{}, err
	}
	if err := q.InsertIntegrationControlReceipt(ctx, dbsqlc.InsertIntegrationControlReceiptParams{
		OrgID: app.OrgID, IntegrationAppID: app.ID, ConnectorKey: app.ConnectorKey, Provider: app.Provider,
		ProviderTenantID: input.ProviderTenantID, EventID: input.EventID, Payload: payload, EndInstallID: endID,
	}); err != nil {
		return IntegrationControlReceipt{}, integrationChannelWriteError("insert integration control receipt", err)
	}
	row, err := q.GetIntegrationControlReceiptByIdentity(ctx, dbsqlc.GetIntegrationControlReceiptByIdentityParams{
		IntegrationAppID: app.ID, EventID: input.EventID,
	})
	if err != nil {
		return IntegrationControlReceipt{}, fmt.Errorf("read integration control receipt: %w", err)
	}
	if row.ProviderTenantID != input.ProviderTenantID || !jsoncanonical.Equal(row.Payload, payload) {
		return IntegrationControlReceipt{}, storeerr.ErrIdempotencyConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return IntegrationControlReceipt{}, fmt.Errorf("commit integration control receipt: %w", err)
	}
	return integrationControlReceiptFromSQLC(row), nil
}

func (s *Store) lockIntegrationControlOwner(
	ctx context.Context, tx pgx.Tx, appID uuid.UUID, capabilities []channelconnector.Capability,
) (IntegrationAppRecord, error) {
	keys, providers, err := normalizedCapabilityColumns(capabilities)
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	row, err := s.q.WithTx(tx).GetConnectorIntegrationApp(ctx, dbsqlc.GetConnectorIntegrationAppParams{
		ID: appID, ConnectorKeys: keys, Providers: providers,
	})
	if err != nil {
		return IntegrationAppRecord{}, integrationChannelReadError("get control receipt app", err)
	}
	app := integrationAppRecordFromSQLC(row)
	if app.OwnerProjectID == uuid.Nil {
		err = lifecyclelock.EnterActiveOrganization(ctx, tx, app.OrgID)
	} else {
		err = lifecyclelock.EnterActiveProject(ctx, tx, app.OrgID, app.OwnerProjectID)
	}
	if err != nil {
		return IntegrationAppRecord{}, err
	}
	return lockProviderControlApp(ctx, tx, app)
}

func connectorInstallationControlScopeEnd(
	ctx context.Context, q *dbsqlc.Queries, app IntegrationAppRecord, tenant string,
) (uuid.UUID, error) {
	endID, err := q.GetConnectorInstallationControlScopeEnd(ctx, dbsqlc.GetConnectorInstallationControlScopeEndParams{
		OrgID: app.OrgID, IntegrationAppID: &app.ID, ProviderTenantID: tenant,
		OwnerProjectID: storeutil.IDFromNil(app.OwnerProjectID),
	})
	if err != nil {
		return uuid.Nil, fmt.Errorf("capture installation control scan bound: %w", err)
	}
	return endID, nil
}

func normalizeIntegrationControlObject(raw json.RawMessage, maximum int) (json.RawMessage, error) {
	object, err := jsoncanonical.ParseObject(raw, maximum)
	if err != nil {
		return nil, storeerr.InvalidRequest(fmt.Errorf("control object: %w", err))
	}
	normalized, err := json.Marshal(object)
	if err != nil {
		return nil, storeerr.InvalidRequest(fmt.Errorf("encode control object: %w", err))
	}
	if err := dbsafe.JSONB(normalized, maximum); err != nil {
		return nil, storeerr.InvalidRequest(fmt.Errorf("control object: %w", err))
	}
	return normalized, nil
}

func integrationControlReceiptFromSQLC(row dbsqlc.IntegrationControlReceipt) IntegrationControlReceipt {
	return IntegrationControlReceipt{
		ID: row.ID, OrgID: row.OrgID, IntegrationAppID: row.IntegrationAppID,
		ConnectorKey: row.ConnectorKey, Provider: row.Provider, ProviderTenantID: row.ProviderTenantID,
		EventID: row.EventID, Payload: row.Payload,
		Progress: IntegrationControlProgress{LastInstallID: row.LastInstallID, EndInstallID: row.EndInstallID},
		State:    IntegrationEventState(row.State), AttemptsSinceProgress: int64(row.AttemptsSinceProgress),
		AvailableAt: row.AvailableAt, LeaseToken: storeutil.IDFromPtr(row.LeaseToken),
		LeaseGeneration: row.LeaseGeneration, LeaseExpiresAt: row.LeaseExpiresAt,
		LastError: row.LastError, CompletedAt: row.CompletedAt, CreatedAt: row.CreatedAt,
	}
}
