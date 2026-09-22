//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestInboxLaunchEventWebhookEnqueueIsAtomicAndReplayable(t *testing.T) {
	t.Parallel()
	app := newAppActivationFixture(t)
	var compiled agentconfig.Compiled
	require.NoError(t, json.Unmarshal(app.profile.CurrentConfig.CompiledDefinition, &compiled))
	compiled.EventWebhook = &agentconfig.EventWebhookCompiled{
		URL: "https://example.com/events", Events: []string{"agent_input"},
	}
	definition := app.encodedDefinition(t, compiled)
	app.profile.CurrentConfig.CompiledDefinition = definition.CompiledDefinition
	f := newInboxLaunchFixtureForApp(t, app, false, time.Minute, "a")
	before, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, f.receipt.ID)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`ALTER TABLE event_webhook_deliveries ADD CONSTRAINT reject_test_deliveries CHECK (false) NOT VALID`)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.ErrorContains(t, err, "enqueue event webhook")
	f.assertAbsent(t, "a")
	assertInboxWebhookProgress(t, f.appActivationFixture, before)
	assertInboxWebhookCount(t, f.appActivationFixture, f.slots["a"].AgentID, "agent_input", 0)
	_, err = f.store.pool.Exec(f.ctx, `ALTER TABLE event_webhook_deliveries DROP CONSTRAINT reject_test_deliveries`)
	require.NoError(t, err)
	result, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.NoError(t, err)
	require.True(t, result.Created)
	assertInboxWebhookCount(t, f.appActivationFixture, result.Agent.ID, "agent_input", 1)
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
	assertInboxWebhookCount(t, f.appActivationFixture, result.Agent.ID, "agent_input", 2)
	replayed, err := f.store.Execution().AdmitInboxLaunchSlot(f.ctx, f.receipt.Lease(), "a")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, result.Agent.ID, replayed.Agent.ID)
	assertInboxWebhookCount(t, f.appActivationFixture, result.Agent.ID, "agent_input", 2)
}

func TestInboxInputEventWebhookEnqueueIsAtomicAndReplayable(t *testing.T) {
	t.Parallel()
	f := newAppInteractionFixture(t)
	enableEventWebhook(t, f.process, []string{"tool_result"})
	prompt := f.question(t)
	_, err := f.store.pool.Exec(f.ctx, `DELETE FROM event_webhook_deliveries WHERE agent_id=$1`, f.process.AgentID)
	require.NoError(t, err)
	slot := inboxInputPlan(t, f.process.AgentID, f.app, "webhook-input")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"}
	receipt := freezeInboxInput(t, f.activation(), slot, "webhook-receipt", time.Minute)
	before, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`ALTER TABLE event_webhook_deliveries ADD CONSTRAINT reject_test_deliveries CHECK (false) NOT VALID`)
	require.NoError(t, err)
	_, err = f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.ErrorContains(t, err, "enqueue event webhook")
	assertInboxWebhookProgress(t, f.activation(), before)
	assertInboxWebhookCount(t, f.activation(), f.process.AgentID, "tool_result", 0)
	require.Equal(t, executionstore.AgentInteractionStateOpen, f.read(t, prompt.ID).State)
	var inputs int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_idempotency_key=$2`,
		f.process.AgentID, slot.Input.IdempotencyKey).Scan(&inputs))
	require.Zero(t, inputs)
	_, err = f.store.pool.Exec(f.ctx, `ALTER TABLE event_webhook_deliveries DROP CONSTRAINT reject_test_deliveries`)
	require.NoError(t, err)

	result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.True(t, result.Created)
	require.Equal(t, []uuid.UUID{prompt.ID}, result.CanceledInteractionIDs)
	require.Equal(t, executionstore.AgentInteractionStateCanceled, f.read(t, prompt.ID).State)
	assertInboxWebhookCount(t, f.activation(), f.process.AgentID, "tool_result", 1)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient")
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, result.AgentInput.ID, replayed.AgentInput.ID)
	assertInboxWebhookCount(t, f.activation(), f.process.AgentID, "tool_result", 1)
}

func assertInboxWebhookProgress(t *testing.T, f appActivationFixture, before integrationstore.IntegrationInboxRecord) {
	t.Helper()
	after, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, before.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(before.Progress), string(after.Progress))
}

func assertInboxWebhookCount(t *testing.T, f appActivationFixture, agentID uuid.UUID, kind string, expected int) {
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
