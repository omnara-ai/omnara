//go:build integration

package executionstore_test

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/crontrigger"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func makeCronDue(t *testing.T, ctx context.Context, f publicChannelFixture, triggerID uuid.UUID) {
	t.Helper()
	_, err := f.store.pool.Exec(ctx, `UPDATE cron_triggers
SET next_fire_after=transaction_timestamp()-interval '1 minute' WHERE id=$1`, triggerID)
	require.NoError(t, err)
}

func claimCron(t *testing.T, ctx context.Context, f publicChannelFixture) executionstore.ClaimedCronTrigger {
	t.Helper()
	result, err := f.store.Execution().ClaimDueCronTriggers(ctx, 10)
	require.NoError(t, err)
	require.Len(t, result.Claimed, 1)
	return result.Claimed[0]
}

func cronLaunchInput(
	t *testing.T, f publicChannelFixture, claim executionstore.ClaimedCronTrigger,
) executionstore.LaunchCronTriggerAgentInput {
	t.Helper()
	tenantID, err := publicid.Encode(publicid.KindOrganization, claim.OrgID)
	require.NoError(t, err)
	actorID, err := publicid.Encode(publicid.KindCronTrigger, claim.TriggerID)
	require.NoError(t, err)
	return executionstore.LaunchCronTriggerAgentInput{
		Trigger: claim, AgentConfigID: f.agent.CurrentConfigID,
		Message: claim.MessageTemplate, IdempotencyKey: "cron-test-firing",
		Actor: &executionstore.ActorParams{
			Provider: executionstore.ActorProviderOmnara, ProviderTenantID: tenantID, ProviderUserID: actorID,
			DisplayName: &claim.Name,
		},
	}
}

func TestCronProfileLaunchCompletionRollsBackAndRetryUsesCurrentBindings(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-atomic-launch")
	second := secondCronChannel(t, ctx, f)
	created, err := f.store.Execution().CreateCronTrigger(ctx, cronChannelInput(f))
	require.NoError(t, err)
	makeCronDue(t, ctx, f, created.ID)
	_, err = f.store.pool.Exec(ctx, `
CREATE FUNCTION fail_profile_cron_completion() RETURNS trigger LANGUAGE plpgsql AS $function$
BEGIN
    IF OLD.claim_token IS NOT NULL AND NEW.claim_token IS NULL
       AND NEW.last_fired_at IS DISTINCT FROM OLD.last_fired_at THEN
        RAISE EXCEPTION 'injected profile cron completion commit failure';
    END IF;
    RETURN NEW;
END
$function$;
CREATE CONSTRAINT TRIGGER profile_cron_completion_failure
AFTER UPDATE ON cron_triggers DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION fail_profile_cron_completion();`)
	require.NoError(t, err)
	service := crontrigger.NewService(f.store.Execution(), nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	stats, err := service.FireDueTriggers(ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Failures: 1}, stats)
	var launches, bindings, inputs int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE id<>$1`, f.agent.ID).Scan(&launches))
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings`).Scan(&bindings))
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id<>$1`, f.agent.ID).Scan(&inputs))
	require.Zero(t, launches, "completion failure rolls back the new agent")
	require.Zero(t, bindings, "completion failure rolls back all channel authority")
	require.Zero(t, inputs, "completion failure rolls back the initial input")
	var claimHeld, fired bool
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT claim_token IS NOT NULL,last_fired_at IS NOT NULL FROM cron_triggers WHERE id=$1`, created.ID,
	).Scan(&claimHeld, &fired))
	require.True(t, claimHeld)
	require.False(t, fired)
	failed, err := f.store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
	require.NoError(t, err)
	require.NotNil(t, failed.FailureReport)
	require.Contains(t, failed.FailureReport.Message, "injected profile cron completion commit failure")
	_, err = f.store.pool.Exec(ctx, `DROP TRIGGER profile_cron_completion_failure ON cron_triggers`)
	require.NoError(t, err)
	newBindings := []executionstore.LaunchChannelBinding{{
		ChannelID: second.ID, Grants: integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true},
	}}
	message := "Updated message."
	_, err = f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, ChannelBindings: &newBindings, MessageTemplate: &message,
	})
	require.NoError(t, err)
	_, err = f.store.pool.Exec(ctx,
		`UPDATE cron_triggers SET claimed_until=transaction_timestamp()-interval '1 second' WHERE id=$1`, created.ID)
	require.NoError(t, err)
	stats, err = service.FireDueTriggers(ctx)
	require.NoError(t, err)
	require.Equal(t, crontrigger.FireStats{Claimed: 1, Launched: 1}, stats)
	var agentID uuid.UUID
	var selected *uuid.UUID
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT id,integration_target_id FROM agents WHERE id<>$1`, f.agent.ID).Scan(&agentID, &selected))
	require.Nil(t, selected, "explicit access does not fabricate a current input channel")
	_, err = f.store.Integrations().GetActiveReceiveBindingForTarget(ctx, testProjectID, agentID, second.ID)
	require.NoError(t, err)
	_, err = f.store.Integrations().GetAgentChannelAccess(ctx, testProjectID, agentID, f.target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "retry used current configuration, not abandoned grants")
	var actualMessage string
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT block.text_content FROM agent_inputs input
JOIN content_blocks block ON block.owner_agent_input_id=input.id AND block.block_kind='text'
WHERE input.agent_id=$1`, agentID).Scan(&actualMessage))
	require.Equal(t, message, actualMessage)
	completed, err := f.store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
	require.NoError(t, err)
	require.NotNil(t, completed.LastFiredAt)
	require.Nil(t, completed.FailureReport)
	stats, err = service.FireDueTriggers(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Claimed, "committed occurrence cannot launch twice")
}

func TestCronProfileLaunchAcceptsClaimedListAndDoesNotRegrantOnReplay(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-accepted-config")
	created, err := f.store.Execution().CreateCronTrigger(ctx, cronChannelInput(f))
	require.NoError(t, err)
	makeCronDue(t, ctx, f, created.ID)
	claim := claimCron(t, ctx, f)
	empty := []executionstore.LaunchChannelBinding{}
	_, err = f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, ChannelBindings: &empty,
	})
	require.NoError(t, err)
	input := cronLaunchInput(t, f, claim)
	launched, err := f.store.Execution().LaunchCronTriggerAgent(ctx, input)
	require.NoError(t, err)
	require.True(t, launched.Created)
	binding, err := f.store.Integrations().GetActiveSendBindingForTarget(
		ctx, testProjectID, launched.Agent.ID, f.target.ID)
	require.NoError(t, err, "an admitted claim may finish with its accepted in-memory list")
	require.True(t, binding.ReadAllowed)
	require.Equal(t, claim.ChannelBindings[0].ReplyChannelGrants, binding.ReplyChannelGrants)
	require.NoError(t, f.store.Integrations().RevokeIntegrationTargetBinding(ctx, testProjectID, binding.ID))
	_, err = f.store.Execution().LaunchCronTriggerAgent(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrConflict, "completion token is consumed by the successful transaction")
	_, err = f.store.Integrations().GetActiveSendBindingForTarget(ctx, testProjectID, launched.Agent.ID, f.target.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "a duplicate attempt cannot restore revoked grants")
	var launches int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE idempotency_key=$1`, input.IdempotencyKey).Scan(&launches))
	require.Equal(t, 1, launches)
	makeCronDue(t, ctx, f, created.ID)
	next := claimCron(t, ctx, f)
	require.Empty(t, next.ChannelBindings, "a later firing reads the changed configuration")
}

func TestCronProfileLaunchLostClaimRollsBack(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-lost-claim")
	created, err := f.store.Execution().CreateCronTrigger(ctx, cronChannelInput(f))
	require.NoError(t, err)
	makeCronDue(t, ctx, f, created.ID)
	old := claimCron(t, ctx, f)
	_, err = f.store.pool.Exec(ctx,
		`UPDATE cron_triggers SET claimed_until=transaction_timestamp()-interval '1 second' WHERE id=$1`, created.ID)
	require.NoError(t, err)
	current := claimCron(t, ctx, f)
	require.NotEqual(t, old.ClaimToken, current.ClaimToken)
	_, err = f.store.Execution().LaunchCronTriggerAgent(ctx, cronLaunchInput(t, f, old))
	require.ErrorIs(t, err, storeerr.ErrConflict)
	var bindings, launches int
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings`).Scan(&bindings))
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE id<>$1`, f.agent.ID).Scan(&launches))
	require.Zero(t, bindings)
	require.Zero(t, launches)
	_, err = f.store.Execution().LaunchCronTriggerAgent(ctx, cronLaunchInput(t, f, current))
	require.NoError(t, err, "the winning claim can commit the same occurrence")
}

func TestCronProfileLaunchRechecksChannelRetirement(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-live-launch")
	created, err := f.store.Execution().CreateCronTrigger(ctx, cronChannelInput(f))
	require.NoError(t, err)
	makeCronDue(t, ctx, f, created.ID)
	claim := claimCron(t, ctx, f)
	require.NoError(t, f.store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
	_, err = f.store.Execution().LaunchCronTriggerAgent(ctx, cronLaunchInput(t, f, claim))
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	var launches int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agents WHERE id<>$1`, f.agent.ID).Scan(&launches))
	require.Zero(t, launches, "an accepted list cannot resurrect retired channel authority")
}
