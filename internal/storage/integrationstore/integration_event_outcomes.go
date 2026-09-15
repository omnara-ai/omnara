package integrationstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationEventOutcomeKey struct {
	ProjectID            ID
	IntegrationInstallID ID
	ReceiptID            ID
	DeliveryKey          string
}

// IntegrationEventOutcome retains only the canonical input. Its immutable
// origin is authoritative even after the destination or binding is retired.
type IntegrationEventOutcome struct {
	AgentID      ID
	AgentInputID ID
}

// GetIntegrationEventOutcomeTx reads replay before mutable recipient checks.
// The caller authenticates receipt scope; this lookup grants no new authority.
func (s *Store) GetIntegrationEventOutcomeTx(
	ctx context.Context,
	tx pgx.Tx,
	key IntegrationEventOutcomeKey,
) (IntegrationEventOutcome, bool, error) {
	if err := validateIntegrationEventOutcomeKey(tx, key); err != nil {
		return IntegrationEventOutcome{}, false, err
	}
	row, err := s.q.WithTx(tx).GetIntegrationEventOutcome(ctx, dbsqlc.GetIntegrationEventOutcomeParams{
		ProjectID: key.ProjectID, IntegrationInstallID: key.IntegrationInstallID,
		ReceiptID: key.ReceiptID, DeliveryKey: key.DeliveryKey,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IntegrationEventOutcome{}, false, nil
	}
	if err != nil {
		return IntegrationEventOutcome{}, false, integrationChannelReadError("get integration event outcome", err)
	}
	return IntegrationEventOutcome{AgentID: row.AgentID, AgentInputID: row.AgentInputID}, true, nil
}

// CreateIntegrationEventOutcomeTx composes with the caller's input transaction.
// The caller holds the receipt FOR SHARE and rechecks its lease before commit.
// This method neither serializes all receipt recipients nor renews a claim.
func (s *Store) CreateIntegrationEventOutcomeTx(
	ctx context.Context,
	tx pgx.Tx,
	key IntegrationEventOutcomeKey,
	outcome IntegrationEventOutcome,
) error {
	if err := validateIntegrationEventOutcomeKey(tx, key); err != nil {
		return err
	}
	if isNilID(outcome.AgentID) || isNilID(outcome.AgentInputID) {
		return storeerr.InvalidRequest(errors.New("canonical agent and input are required"))
	}
	if err := s.q.WithTx(tx).InsertIntegrationEventOutcome(ctx, dbsqlc.InsertIntegrationEventOutcomeParams{
		ProjectID: key.ProjectID, IntegrationInstallID: key.IntegrationInstallID,
		ReceiptID: key.ReceiptID, DeliveryKey: key.DeliveryKey,
		AgentID: outcome.AgentID, AgentInputID: outcome.AgentInputID,
	}); err != nil {
		return integrationChannelWriteError("create integration event outcome", err)
	}
	// A second statement sees a concurrently committed winner under READ COMMITTED.
	canonical, found, err := s.GetIntegrationEventOutcomeTx(ctx, tx, key)
	if err != nil {
		return err
	}
	if !found {
		return storeerr.ErrNotFound
	}
	if canonical != outcome {
		return storeerr.ErrIdempotencyConflict
	}
	return nil
}

func validateIntegrationEventOutcomeKey(tx pgx.Tx, key IntegrationEventOutcomeKey) error {
	if tx == nil || isNilID(key.ProjectID) || isNilID(key.IntegrationInstallID) || isNilID(key.ReceiptID) ||
		key.DeliveryKey == "" || len(key.DeliveryKey) > 512 {
		return storeerr.InvalidRequest(
			errors.New("transaction, project, connection, receipt and bounded delivery key are required"))
	}
	if err := dbsafe.Text(key.DeliveryKey); err != nil {
		return storeerr.InvalidRequest(err)
	}
	return nil
}
