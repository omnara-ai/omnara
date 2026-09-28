//go:build integration

package integration

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInboxConversationQuotaPreservesObserversAndRetriesLaunch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"unsubscribe then retry", "retry budget exhausted"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newChoiceJourney(t, 1)
			observers := make([]uuid.UUID, 16)
			for i := range observers {
				observers[i] = launcherObserver(t, f).ID
			}
			inbox := f.store.Integrations()
			f.provider.event = &f.event
			input := integrationstore.VerifiedIntegrationReceipt{
				ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID,
				ReceiptKey: "full-conversation", Payload: []byte(`{"original":true}`),
			}
			receipt, created, err := inbox.AcceptIntegrationReceipt(ctx, input)
			require.NoError(t, err)
			require.True(t, created)
			worker := NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
			worked, err := worker.RunOnce(ctx)
			require.True(t, worked)
			require.ErrorIs(t, err, storeerr.ErrConflict)
			const capacityError = "integration conversation limit of 16 subscribed agents reached"
			require.ErrorContains(t, err, capacityError)
			first, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxQueued, first.State)
			require.Equal(t, 1, first.AttemptCount)
			require.Nil(t, first.CompletedAt, "capacity failure must not terminate the receipt immediately")
			require.Contains(t, first.LastError, capacityError)
			delay := first.NextAttemptAt.Sub(first.UpdatedAt)
			require.Positive(t, delay, "capacity failures retain retry backoff")
			require.LessOrEqual(t, delay, integrationstore.IntegrationInboxMaxRetryDelay)
			require.Empty(t, f.provider.notices, "no terminal failure notice before the retry budget is exhausted")

			var plan IntegrationInboxPlan
			require.NoError(t, json.Unmarshal(first.Plan, &plan))
			require.Len(t, plan.Recipients, 17, "observers must not suppress the launch intent")
			outcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(ctx, first)
			require.NoError(t, err)
			var launchID uuid.UUID
			var launchKey string
			for key, slot := range plan.Recipients {
				if slot.Launch != nil {
					require.Equal(t, uuid.Nil, launchID, "the receipt must contain exactly one launch")
					launchID, launchKey = slot.AgentID, key
					require.Equal(t, executionstore.InboxSlotPending, outcomes[key])
					continue
				}
				require.Contains(t, observers, slot.AgentID)
				require.Equal(t, executionstore.InboxSlotDelivered, outcomes[key])
			}
			require.NotEqual(t, uuid.Nil, launchID)
			_, err = f.store.Execution().GetAgentInProject(ctx, f.ids.ProjectID, launchID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "quota failure must roll back the planned seventeenth agent")
			var agents int
			require.NoError(t, f.pool.QueryRow(ctx,
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Equal(t, 16, agents)
			readInputs := func() map[uuid.UUID]uuid.UUID {
				t.Helper()
				rows, err := f.pool.Query(ctx,
					`SELECT agent_id,id FROM agent_inputs WHERE project_id=$1 AND input_kind='content'`, f.ids.ProjectID)
				require.NoError(t, err)
				defer rows.Close()
				inputs := make(map[uuid.UUID]uuid.UUID)
				for rows.Next() {
					var agentID, inputID uuid.UUID
					require.NoError(t, rows.Scan(&agentID, &inputID))
					require.NotContains(t, inputs, agentID, "retry must not duplicate an observer input")
					inputs[agentID] = inputID
				}
				require.NoError(t, rows.Err())
				return inputs
			}
			before := readInputs()
			require.Len(t, before, 16)
			for _, observer := range observers {
				require.Contains(t, before, observer, "all observers receive despite the failed launch")
			}

			if scenario == "unsubscribe then retry" {
				removeTestAgentSubscriptions(t, f.store, f.integration, observers[0])
			} else {
				// Exercise the final budgeted attempt without waiting through every backoff.
				_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET attempt_count=$2 WHERE id=$1`,
					receipt.ID, integrationstore.IntegrationInboxMaxAttempts-1)
				require.NoError(t, err)
			}
			_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET next_attempt_at=now() WHERE id=$1`, receipt.ID)
			require.NoError(t, err)
			expansions := f.provider.expansions
			f.restart()
			worker = NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
			worked, retryErr := worker.RunOnce(ctx)
			require.True(t, worked)
			after, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.NotNil(t, after.CompletedAt)
			require.JSONEq(t, string(first.Plan), string(after.Plan), "retry must retain the original recipients")
			require.Equal(t, expansions, f.provider.expansions, "retry must use the frozen event")
			current := readInputs()
			for observer, inputID := range before {
				require.Equal(t, inputID, current[observer], "delivered observers retain their original input")
			}
			finalOutcomes, err := f.store.Execution().GetIntegrationInboxOutcomes(ctx, after)
			require.NoError(t, err)
			if scenario == "unsubscribe then retry" {
				require.NoError(t, retryErr)
				require.Equal(t, integrationstore.IntegrationInboxCompleted, after.State)
				require.Equal(t, 2, after.AttemptCount)
				require.Len(t, current, 17)
				require.Contains(t, current, launchID)
				require.Equal(t, executionstore.InboxSlotDelivered, finalOutcomes[launchKey])
				launched, err := f.store.Execution().GetAgentInProject(ctx, f.ids.ProjectID, launchID)
				require.NoError(t, err)
				require.Equal(t, f.profiles[0].ID, launched.AgentProfileID)
				subscriptions, err := inbox.ListIntegrationSubscriptions(ctx,
					integrationstore.ListIntegrationSubscriptionsInput{
						ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, Limit: 100,
					})
				require.NoError(t, err)
				require.Len(t, subscriptions.Subscriptions, 16)
				require.Empty(t, f.provider.notices)
			} else {
				require.ErrorIs(t, retryErr, storeerr.ErrConflict)
				require.ErrorContains(t, retryErr, capacityError)
				require.Equal(t, integrationstore.IntegrationInboxFailed, after.State)
				require.Equal(t, integrationstore.IntegrationInboxMaxAttempts, after.AttemptCount)
				require.Equal(t, before, current)
				require.Equal(t, outcomes, finalOutcomes)
				_, err = f.store.Execution().GetAgentInProject(ctx, f.ids.ProjectID, launchID)
				require.ErrorIs(t, err, pgx.ErrNoRows)
				require.Equal(t, []string{"Request failed"}, f.provider.notices)
			}
			duplicate, created, err := inbox.AcceptIntegrationReceipt(ctx, input)
			require.NoError(t, err)
			require.False(t, created)
			require.Equal(t, receipt.ID, duplicate.ID)
			worked, err = worker.RunOnce(ctx)
			require.NoError(t, err)
			require.False(t, worked, "a terminal receipt must not start another attempt")
			require.Equal(t, current, readInputs())
		})
	}
}
