//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestLifecycleUnitEnqueuesWebhooksAtomically(t *testing.T) {
	for _, scope := range []string{"project", "organization"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail_%t", scope, fail), func(t *testing.T) {
				ctx := t.Context()
				f := newProcessDaemonFixture(t, ctx, "lifecycle-webhooks")
				toolID := createToolCallForProcessTestWithPermission(t, ctx, f, "lifecycle-question", "ask_question", true)
				enableEventWebhook(t, f, []string{"agent_input", "tool_result", "tool_call_update"})
				question := createQuestionInteractionForTest(t, ctx, f, toolID)
				publisher := &recordingPostCommitPublisher{}
				f.Store = newIntegrationStore(f.Store.pool, storage.WithPostCommitPublisher(publisher))
				_, err := f.Store.pool.Exec(ctx, `DELETE FROM event_webhook_deliveries WHERE agent_id=$1`, f.AgentID)
				require.NoError(t, err)
				if fail {
					_, err := f.Store.pool.Exec(ctx, `
CREATE FUNCTION reject_unit_webhook() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN RAISE EXCEPTION 'rejected webhook'; END $$;
CREATE TRIGGER reject_unit_webhook BEFORE INSERT ON event_webhook_deliveries
FOR EACH ROW EXECUTE FUNCTION reject_unit_webhook();`)
					require.NoError(t, err)
				}
				remove := func(ctx context.Context) error {
					if scope == "project" {
						_, err := f.Store.Organizations().DeleteProject(ctx, testOrgID, testProjectID, userPrincipal(f.UserID))
						return err
					}
					_, err := f.Store.Organizations().DeleteOrganization(ctx, testOrgID, userPrincipal(f.UserID))
					return err
				}
				err = remove(ctx)
				if fail {
					require.ErrorContains(t, err, "EnqueueEventWebhookDelivery")
				} else {
					require.NoError(t, err)
				}
				var state string
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT state FROM agents WHERE id=$1`, f.AgentID).Scan(&state))
				var count int
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1`, f.AgentID).Scan(&count))
				if fail {
					require.Equal(t, "active", state)
					require.Zero(t, count)
					require.Empty(t, publisher.intents)
					var interactionState string
					require.NoError(t, f.Store.pool.QueryRow(ctx,
						`SELECT state FROM agent_interactions WHERE id=$1`, question.ID).Scan(&interactionState))
					require.Equal(t, string(executionstore.AgentInteractionStateOpen), interactionState)
					return
				}
				require.Equal(t, "archived", state)
				require.Positive(t, count)
				var completed int
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1 AND tool_call_id=$2 AND tool_state='completed'`, f.AgentID, toolID).Scan(&completed))
				require.Equal(t, 1, completed)
				var published bool
				for _, intent := range publisher.intents {
					if update, ok := intent.(notifications.ToolCallUpdatedCommitted); ok &&
						update.ToolCallID == toolID && update.State == "completed" {
						published = true
					}
				}
				require.True(t, published)
			})
		}
	}
}
