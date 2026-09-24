//go:build integration

package executionstore_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestInboxInputArchivedRecipientSettlesWithoutPreparation(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "archived-recipient"))
	require.NoError(t, err)
	slot := withInboxFile(t, inboxInputPlan(t, agent.Agent.ID, f.integration, "message:archived"))
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	receipt := freezeInboxInput(t, f, slot, "archived-recipient", time.Minute)
	_, _, err = f.store.Execution().ArchiveAgent(f.ctx, testProjectID, agent.Agent.ID, userPrincipal(f.user.ID))
	require.NoError(t, err)
	result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient", nil)
	require.NoError(t, err, "archived recipients need no provider preparation")
	require.Equal(t, executionstore.InboxInputResult{Skipped: executionstore.InboxInputSkipAgentArchived}, result)
	require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, receipt.Lease(), "recipient", slot.Input.Origin.Address), executionstore.ErrInboxRecipientSettled)
	require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, receipt.Lease(), "recipient", integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:9.9"}),
		storeerr.ErrUnauthorized, "settled outcomes never authorize another conversation")
	saved, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
	require.NoError(t, err)
	outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(f.ctx, saved)
	require.NoError(t, err)
	require.Equal(t, executionstore.InboxSlotSkipped, outcomes["recipient"])
	for _, table := range []string{"agent_inputs", "artifacts", "integration_targets"} {
		var count int
		query := "SELECT count(*) FROM " + table + " WHERE agent_id=$1"
		if table == "agent_inputs" {
			query += " AND input_kind='content'"
		}
		require.NoError(t, f.store.pool.QueryRow(f.ctx, query, agent.Agent.ID).Scan(&count))
		require.Zero(t, count, table)
	}
	require.NoError(t, f.store.Execution().CompleteIntegrationInbox(f.ctx, receipt.Lease()))
	f.disable(t)
	replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient", nil)
	require.NoError(t, err, "completed skips replay without reacquiring authority")
	require.Equal(t, result, replayed)
}

func TestInboxInputArchivedRecipientReplaysPriorDelivery(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"same input", "message sibling", "supplemental files"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationActivationFixture(t)
			agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "prior-recipient"))
			require.NoError(t, err)
			slot := inboxInputPlan(t, agent.Agent.ID, f.integration, "message:prior")
			slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
			first := freezeInboxInput(t, f, slot, "prior-delivery", time.Minute)
			prior, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, first.Lease(), "recipient", nil)
			require.NoError(t, err)
			require.True(t, prior.Created)
			if scenario != "same input" {
				slot.Sibling = &executionstore.InboxMessageSibling{Key: slot.Input.IdempotencyKey}
				slot.Input.IdempotencyKey = "mention:prior"
			}
			if scenario == "supplemental files" {
				slot = withInboxFile(t, slot)
				slot.Sibling.AttachmentNotice = "Additional attachments"
			}
			pending := freezeInboxInput(t, f, slot, "duplicate-delivery", time.Minute)
			_, _, err = f.store.Execution().ArchiveAgent(f.ctx, testProjectID, agent.Agent.ID, userPrincipal(f.user.ID))
			require.NoError(t, err)
			result, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, pending.Lease(), "recipient", nil)
			require.NoError(t, err)
			require.False(t, result.Created)
			if scenario == "supplemental files" {
				require.Equal(t, executionstore.InboxInputSkipAgentArchived, result.Skipped)
				require.Equal(t, uuid.Nil, result.AgentInput.ID)
			} else {
				require.Empty(t, result.Skipped, "already delivered input stays delivered")
				require.Equal(t, prior.AgentInput.ID, result.AgentInput.ID)
			}
			replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, pending.Lease(), "recipient", nil)
			require.NoError(t, err)
			require.Equal(t, result.Skipped, replayed.Skipped)
			require.Equal(t, result.AgentInput.ID, replayed.AgentInput.ID)
			var count int
			require.NoError(t, f.store.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`, agent.Agent.ID).Scan(&count))
			require.Equal(t, 1, count)
		})
	}
}

func TestInboxInputArchiveRechecksStateAfterAgentLockWait(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "archive-race"))
	require.NoError(t, err)
	slot := inboxInputPlan(t, agent.Agent.ID, f.integration, "message:archive-race")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	receipt := freezeInboxInput(t, f, slot, "archive-race", time.Minute)
	blocker := integrationdb.BeginTx(t, f.ctx, f.store.pool)
	_, err = dbsqlc.New(blocker).LockAgentInProject(f.ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: agent.Agent.ID})
	require.NoError(t, err)
	archiveActor := mustOmnaraActorParams(t, f.user.ID)
	archive := integrationdb.RunAsyncError(func() error {
		_, _, err := f.store.Execution().IntegrationArchiveAgentOnce(
			f.ctx, testOrgID, testProjectID, agent.Agent.ID, archiveActor,
		)
		return err
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 1)
	admission := integrationdb.RunAsync(func() (executionstore.InboxInputResult, error) {
		return f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient", nil)
	})
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.store.pool, "LockAgentInProject", 2)
	require.NoError(t, blocker.Commit(f.ctx))
	require.NoError(t, integrationdb.Await(t, archive, "agent archival"))
	result := integrationdb.AwaitSuccess(t, admission, "archive wins admission race")
	require.Equal(t, executionstore.InboxInputSkipAgentArchived, result.Skipped)
	require.False(t, result.Created)
	var targets, inputs int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM integration_targets WHERE agent_id=$1`, agent.Agent.ID).Scan(&targets))
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`, agent.Agent.ID).Scan(&inputs))
	require.Zero(t, targets, "inbox settlement must not create a target after archival")
	require.Zero(t, inputs, "inbox settlement must not admit content after archival")
	saved, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
	require.NoError(t, err)
	outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(f.ctx, saved)
	require.NoError(t, err)
	require.Equal(t, executionstore.InboxSlotSkipped, outcomes["recipient"])
}

func TestInboxInputMissingRecipientDoesNotSettle(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	slot := inboxInputPlan(t, uuid.New(), f.integration, "message:missing")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	receipt := freezeInboxInput(t, f, slot, "missing-recipient", time.Minute)
	_, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, receipt.Lease(), "recipient", nil)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	saved, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, receipt.ID)
	require.NoError(t, err)
	outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(f.ctx, saved)
	require.NoError(t, err)
	require.Equal(t, executionstore.InboxSlotPending, outcomes["recipient"],
		"a missing recipient is pending, never skipped")
	require.ErrorIs(t, f.store.Execution().CompleteIntegrationInbox(f.ctx, receipt.Lease()),
		storeerr.ErrStateTransitionConflict)
}

func TestInboxInputOutcomesSurviveArchivalAndDeletedTargets(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, f.launchInput(f.profile.CurrentConfigID, "historical-recipient"))
	require.NoError(t, err)
	slot := inboxInputPlan(t, agent.Agent.ID, f.integration, "message:delivered")
	slot.Input.Origin.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	delivered := freezeInboxInput(t, f, slot, "historical-delivered", time.Minute)
	input, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, delivered.Lease(), "recipient", nil)
	require.NoError(t, err)
	slot.Input.IdempotencyKey = "message:skipped"
	slot = withInboxFile(t, slot)
	pending := freezeInboxInput(t, f, slot, "historical-skipped", time.Minute)
	_, _, err = f.store.Execution().ArchiveAgent(f.ctx, testProjectID, agent.Agent.ID, userPrincipal(f.user.ID))
	require.NoError(t, err)
	// Resolve archival before any admission or upload attempt for the pending slot.
	require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(
		f.ctx, pending.Lease(), "recipient", slot.Input.Origin.Address), executionstore.ErrInboxRecipientSettled)
	require.NoError(t, f.store.Execution().CompleteIntegrationInbox(f.ctx, pending.Lease()))
	require.NoError(t, f.store.Execution().CompleteIntegrationInbox(f.ctx, delivered.Lease()))
	require.NoError(t, f.store.Integrations().DeleteProjectIntegration(f.ctx, testOrgID, testProjectID, f.integration.ID))
	var deleted bool
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT deleted_at IS NOT NULL FROM integration_targets WHERE id=$1`, input.AgentInput.IntegrationTargetID,
	).Scan(&deleted))
	require.True(t, deleted)
	for _, tc := range []struct {
		receipt integrationstore.IntegrationInboxRecord
		outcome executionstore.InboxSlotOutcome
	}{
		{delivered, executionstore.InboxSlotDelivered},
		{pending, executionstore.InboxSlotSkipped},
	} {
		saved, err := f.store.Integrations().GetIntegrationInbox(f.ctx, testProjectID, tc.receipt.ID)
		require.NoError(t, err)
		outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(f.ctx, saved)
		require.NoError(t, err)
		require.Equal(t, tc.outcome, outcomes["recipient"])
		replayed, err := f.store.Execution().AdmitInboxInputSlot(f.ctx, tc.receipt.Lease(), "recipient", nil)
		require.NoError(t, err, "settled work replays after its lease and live authority end")
		require.False(t, replayed.Created)
		if tc.outcome == executionstore.InboxSlotDelivered {
			require.Equal(t, input.AgentInput.ID, replayed.AgentInput.ID)
			require.Equal(t, input.AgentInput.IntegrationTargetID, replayed.AgentInput.IntegrationTargetID)
			require.Empty(t, replayed.Skipped)
		} else {
			require.Equal(t, executionstore.InboxInputSkipAgentArchived, replayed.Skipped)
			require.Equal(t, uuid.Nil, replayed.AgentInput.ID)
		}
		require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(
			f.ctx, tc.receipt.Lease(), "recipient", slot.Input.Origin.Address), executionstore.ErrInboxRecipientSettled)
		require.ErrorIs(t, f.store.Execution().CheckInboxConversationAuthority(f.ctx, tc.receipt.Lease(), "recipient",
			integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:9.9"}), storeerr.ErrUnauthorized)
	}
}
