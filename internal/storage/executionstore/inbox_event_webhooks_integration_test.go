//go:build integration

package executionstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestInboxLaunchEventWebhookEnqueueIsAtomicAndReplayable(t *testing.T) {
	t.Parallel()
	integration := newIntegrationActivationFixture(t)
	_, err := integration.store.Execution().CreateEventWebhook(t.Context(), executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Type: executionstore.EventWebhookScopeAgentProfile,
			ID:   integration.profile.ID,
		},
		Settings: webhookconfig.Settings{URL: "https://example.com/events", EventTypes: []string{"agent_input"}},
	})
	require.NoError(t, err)
	f := newInboxLaunchFixtureForIntegration(t, integration, false, time.Minute, "a")
	before, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, f.receipt.ID)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`ALTER TABLE event_webhook_deliveries ADD CONSTRAINT reject_test_deliveries CHECK (false) NOT VALID`)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchRecipient(f.ctx, f.receipt.Lease(), "a", nil)
	require.ErrorContains(t, err, "enqueue event webhook delivery")
	f.assertAbsent(t, "a")
	assertInboxWebhookPending(t, f.integrationActivationFixture, before)
	assertInboxWebhookDeliveryCount(t, f.integrationActivationFixture, f.recipients["a"].AgentID, "agent_input", 0)
	_, err = f.store.pool.Exec(f.ctx, `ALTER TABLE event_webhook_deliveries DROP CONSTRAINT reject_test_deliveries`)
	require.NoError(t, err)
	result, err := f.store.Execution().AdmitInboxLaunchRecipient(f.ctx, f.receipt.Lease(), "a", nil)
	require.NoError(t, err)
	require.True(t, result.Created)
	assertInboxWebhookDeliveryCount(t, f.integrationActivationFixture, result.Agent.ID, "agent_input", 1)
	var sequence int64
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT event_sequence FROM event_webhook_deliveries WHERE agent_id=$1`, result.Agent.ID).Scan(&sequence))
	require.Equal(t, result.ConfigChange.Event.Sequence, sequence)

	claim, found, err := f.store.Execution().ClaimNextAgentWork(f.ctx, testClaimNextAgentWorkInput())
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.AgentWorkModel, claim.Kind)
	require.Len(t, claim.Model.AdmittedInputTurn.Inputs, 1)
	require.Equal(t, result.AgentInput.ID, claim.Model.AdmittedInputTurn.Inputs[0].ID)
	assertInboxWebhookDeliveryCount(t, f.integrationActivationFixture, result.Agent.ID, "agent_input", 2)
	replayed, err := f.store.Execution().AdmitInboxLaunchRecipient(f.ctx, f.receipt.Lease(), "a", nil)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, result.Agent.ID, replayed.Agent.ID)
	assertInboxWebhookDeliveryCount(t, f.integrationActivationFixture, result.Agent.ID, "agent_input", 2)
}

func TestInboxInputEventWebhookEnqueueIsAtomicAndReplayable(t *testing.T) {
	t.Parallel()
	f := newIntegrationInteractionFixture(t)
	createAgentEventWebhook(t, f.process, []string{"tool_result"})
	prompt := f.question(t)
	_, err := f.store.pool.Exec(f.ctx, `DELETE FROM event_webhook_deliveries WHERE agent_id=$1`, f.process.AgentID)
	require.NoError(t, err)
	recipient := inboxInputPlan(t, f.process.AgentID, f.integration, "webhook-input")
	recipient.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
	receipt := freezeInboxInput(t, f.activation(), recipient, "webhook-receipt", time.Minute)
	before, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`ALTER TABLE event_webhook_deliveries ADD CONSTRAINT reject_test_deliveries CHECK (false) NOT VALID`)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxInputRecipient(f.ctx, receipt.Lease(), "recipient", nil)
	require.ErrorContains(t, err, "enqueue event webhook delivery")
	assertInboxWebhookPending(t, f.activation(), before)
	assertInboxWebhookDeliveryCount(t, f.activation(), f.process.AgentID, "tool_result", 0)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, prompt.ID).State)
	var inputs int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_idempotency_key=$2`,
		f.process.AgentID, recipient.Input.IdempotencyKey).Scan(&inputs))
	require.Zero(t, inputs)
	_, err = f.store.pool.Exec(f.ctx, `ALTER TABLE event_webhook_deliveries DROP CONSTRAINT reject_test_deliveries`)
	require.NoError(t, err)

	result, err := f.store.Execution().AdmitInboxInputRecipient(f.ctx, receipt.Lease(), "recipient", nil)
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Equal(t, []uuid.UUID{prompt.ID}, result.CanceledInteractionIDs)
	require.Equal(t, executionstore.AgentInteractionStateCanceled, f.read(t, prompt.ID).State)
	assertInboxWebhookDeliveryCount(t, f.activation(), f.process.AgentID, "tool_result", 1)
	replayed, err := f.store.Execution().AdmitInboxInputRecipient(f.ctx, receipt.Lease(), "recipient", nil)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, result.AgentInput.ID, replayed.AgentInput.ID)
	assertInboxWebhookDeliveryCount(t, f.activation(), f.process.AgentID, "tool_result", 1)
}

func assertInboxWebhookPending(
	t *testing.T,
	f integrationActivationFixture,
	before integrationstore.IntegrationInboxRecord,
) {
	t.Helper()
	after, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, before.ID)
	require.NoError(t, err)
	outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(f.ctx, after)
	require.NoError(t, err)
	require.NotEmpty(t, outcomes)
	for _, outcome := range outcomes {
		require.Equal(t, executionstore.InboxRecipientPending, outcome)
	}
}

func assertInboxWebhookDeliveryCount(
	t *testing.T,
	f integrationActivationFixture,
	agentID uuid.UUID,
	kind string,
	expected int,
) {
	t.Helper()
	var total, matching int
	require.NoError(t, f.store.pool.QueryRow(f.ctx, `
		SELECT count(*), count(event.id)
		FROM event_webhook_deliveries delivery
		LEFT JOIN agent_events event ON event.agent_id=delivery.agent_id AND event.sequence=delivery.event_sequence
		    AND event.event_kind=$3
		WHERE delivery.agent_id=$1 AND delivery.org_id=$2`, agentID, testOrgID, kind).Scan(&total, &matching))
	require.Equal(t, expected, total)
	require.Equal(t, expected, matching, "each delivery must reference the durable event committed by admission")
}
