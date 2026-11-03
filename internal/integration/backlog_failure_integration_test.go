//go:build integration

package integration

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInboxFullBacklogExhaustionNotice(t *testing.T) {
	ctx := t.Context()
	f := newChoiceJourney(t, 1)
	agent := launcherObserver(t, f)
	for range 50 {
		_, _, _, err := f.store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID: f.ids.ProjectID, AgentID: agent.ID, ContentBlocks: []byte(`[{"type":"text","text":"pending"}]`),
		})
		require.NoError(t, err)
	}
	f.provider.event = &f.event
	inbox := f.store.Integrations()
	receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: f.ids.ProjectID, IntegrationID: f.integration.ID, ReceiptKey: "backlog-exhausted", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	worker := NewIntegrationInboxWorker(inbox, f.consumer, IntegrationInboxWorkerOptions{})
	for attempt := 1; attempt <= integrationstore.IntegrationInboxMaxAttempts; attempt++ {
		worked, err := worker.RunOnce(ctx)
		require.True(t, worked)
		require.ErrorIs(t, err, storeerr.ErrAgentBacklogFull)
		current, err := inbox.GetIntegrationInbox(ctx, f.ids.ProjectID, receipt.ID)
		require.NoError(t, err)
		require.Equal(t, attempt, current.AttemptCount)
		if attempt < integrationstore.IntegrationInboxMaxAttempts {
			require.Empty(t, f.provider.notices)
			require.Equal(t, integrationstore.IntegrationInboxQueued, current.State)
			_, err = f.pool.Exec(ctx, `UPDATE integration_inbox SET next_attempt_at=now() WHERE id=$1`, receipt.ID)
			require.NoError(t, err)
		} else {
			require.Equal(t, integrationstore.IntegrationInboxFailed, current.State)
			require.Equal(t, []string{partialInboxDeliveryMessage}, f.provider.notices)
		}
	}
}
