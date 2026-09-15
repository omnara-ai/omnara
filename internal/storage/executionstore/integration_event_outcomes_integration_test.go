//go:build integration

package executionstore_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationEventOutcomesRetainCanonicalInputUntilReceiptRetention(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "receipt-outcome-retention")
	event := f.event(t, ctx, "first")
	delivered, err := f.Store.Execution().DeliverChannelWorkflow(ctx, event)
	require.NoError(t, err)
	input := integrationstore.IntegrationEventOutcomeKey{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID, ReceiptID: event.Receipt.ReceiptID,
		DeliveryKey: "binding:" + delivered.BindingID.String(),
	}
	outcome := integrationstore.IntegrationEventOutcome{
		AgentID: delivered.AgentInput.AgentID, AgentInputID: delivered.AgentInput.ID,
	}
	create := func(key integrationstore.IntegrationEventOutcomeKey, value integrationstore.IntegrationEventOutcome) error {
		tx, err := f.Store.pool.Begin(ctx)
		if err != nil {
			return err
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if err := f.Store.Integrations().CreateIntegrationEventOutcomeTx(ctx, tx, key, value); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	require.NoError(t, create(input, outcome))
	require.NoError(t, create(input, outcome))
	second := f.event(t, ctx, "second")
	other, err := f.Store.Execution().DeliverChannelWorkflow(ctx, second)
	require.NoError(t, err)
	conflict := outcome
	conflict.AgentInputID = other.AgentInput.ID
	err = create(input, conflict)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)
	wrongScope := input
	wrongScope.ProjectID = uuid.New()
	err = create(wrongScope, outcome)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "receipt and canonical agent must share the project")
	wrongInstall := input
	wrongInstall.IntegrationInstallID = uuid.New()
	require.ErrorIs(t, create(wrongInstall, outcome), storeerr.ErrNotFound)
	_, otherAgent, _, _ := createChannelLifecycleFixture(t, ctx, f.Store, "receipt-outcome-other-agent")
	wrongOwner := outcome
	wrongOwner.AgentID = otherAgent.ID
	wrongKey := input
	wrongKey.DeliveryKey += ":wrong-owner"
	err = create(wrongKey, wrongOwner)
	require.True(t, isPgCode(err, "23503"), "the input must belong to the canonical agent: %v", err)
	_, err = f.Store.pool.Exec(ctx, `UPDATE integration_event_outcomes SET agent_input_id = $1
		WHERE receipt_id = $2 AND delivery_key = $3`, other.AgentInput.ID, input.ReceiptID, input.DeliveryKey)
	require.True(t, isPgCode(err, "25006"), "outcome identity is immutable: %v", err)

	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, delivered.BindingID))
	_, err = f.Store.pool.Exec(ctx,
		`UPDATE integration_targets SET deleted_at = statement_timestamp() WHERE id = $1`, delivered.ChannelID)
	require.NoError(t, err)
	tx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	retained, found, err := f.Store.Integrations().GetIntegrationEventOutcomeTx(ctx, tx, input)
	require.NoError(t, err, "receipt replay is independent of current recipient authority")
	require.True(t, found)
	require.Equal(t, outcome, retained)
	require.NoError(t, tx.Rollback(ctx))
	_, err = f.Store.Integrations().FinishIntegrationEvent(ctx, integrationstore.FinishIntegrationEventInput{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
		ID: input.ReceiptID, LeaseToken: event.Receipt.LeaseToken, LeaseGeneration: event.Receipt.LeaseGeneration,
		State: integrationstore.IntegrationEventCompleted, Capabilities: f.Identity.Capabilities,
	})
	require.NoError(t, err)
	retention := integrationstore.DeleteRetainedIntegrationEventsInput{Retention: 7 * 24 * time.Hour, Limit: 10}
	deleted, err := f.Store.Integrations().DeleteRetainedIntegrationEvents(ctx, retention)
	require.NoError(t, err)
	require.Zero(t, deleted, "fresh terminal receipts retain event-level replay")
	_, err = f.Store.pool.Exec(ctx, `UPDATE integration_event_receipts
		SET completed_at = statement_timestamp() - interval '8 days' WHERE id = $1`, input.ReceiptID)
	require.NoError(t, err)
	deleted, err = f.Store.Integrations().DeleteRetainedIntegrationEvents(ctx, retention)
	require.NoError(t, err)
	require.EqualValues(t, 1, deleted)
	var outcomes, inputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM integration_event_outcomes WHERE receipt_id = $1`, input.ReceiptID).Scan(&outcomes))
	require.Zero(t, outcomes, "only receipt retention cascades away outcome replay")
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE id = $1`, outcome.AgentInputID).Scan(&inputs))
	require.Equal(t, 1, inputs, "canonical execution history survives receipt retention")
}

func TestIntegrationEventOutcomesDoNotSerializeIndependentRecipients(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newChannelWorkflowFixture(t, ctx, "receipt-outcome-concurrency")
	event := f.event(t, ctx, "first")
	delivered, err := f.Store.Execution().DeliverChannelWorkflow(ctx, event)
	require.NoError(t, err)
	input := integrationstore.IntegrationEventOutcomeKey{
		ProjectID: testProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID, ReceiptID: event.Receipt.ReceiptID,
		DeliveryKey: "binding:" + uuid.NewString(),
	}
	outcome := integrationstore.IntegrationEventOutcome{
		AgentID: delivered.AgentInput.AgentID, AgentInputID: delivered.AgentInput.ID,
	}
	tx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SELECT id FROM integration_event_receipts WHERE id = $1 FOR SHARE`, input.ReceiptID)
	require.NoError(t, err)
	err = f.Store.Integrations().CreateIntegrationEventOutcomeTx(ctx, tx, input, outcome)
	require.NoError(t, err)
	// The first recipient remains uncommitted while a different key commits.
	otherCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	otherTx, err := f.Store.pool.Begin(otherCtx)
	require.NoError(t, err)
	defer func() { _ = otherTx.Rollback(ctx) }()
	_, err = otherTx.Exec(otherCtx, `SELECT id FROM integration_event_receipts WHERE id = $1 FOR SHARE`, input.ReceiptID)
	require.NoError(t, err)
	otherInput := input
	otherInput.DeliveryKey = "binding:" + uuid.NewString()
	err = f.Store.Integrations().CreateIntegrationEventOutcomeTx(otherCtx, otherTx, otherInput, outcome)
	require.NoError(t, err, "independent recipients must not require an exclusive receipt lock")
	require.NoError(t, otherTx.Commit(otherCtx))
	require.NoError(t, tx.Rollback(ctx))
	readTx, err := f.Store.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = readTx.Rollback(ctx) }()
	_, found, err := f.Store.Integrations().GetIntegrationEventOutcomeTx(ctx, readTx, input)
	require.NoError(t, err)
	require.False(t, found, "failed input transaction cannot leave an outcome")
	_, found, err = f.Store.Integrations().GetIntegrationEventOutcomeTx(ctx, readTx, otherInput)
	require.NoError(t, err)
	require.True(t, found)
}
