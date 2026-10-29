//go:build integration

package executionstore_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestLifecycleDeletionSuppressesWebhooks(t *testing.T) {
	for _, scope := range []string{"project", "organization"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail_%t", scope, fail), func(t *testing.T) {
				ctx := t.Context()
				f := newProcessDaemonFixture(t, ctx, "lifecycle-webhooks")
				owner := createSecretTestUser(t, ctx, f.Store, "Lifecycle Webhooks Owner", "owner")
				toolID := createToolCallForProcessTestWithPermission(t, ctx, f, "lifecycle-question", "ask_question", true)
				createAgentEventWebhook(t, f, []string{"agent_input", "tool_result", "tool_call_update"})
				createQuestionInteractionForTest(t, ctx, f, toolID)
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
					_, err := f.Store.Organizations().DeleteOrganization(ctx, testOrgID, userPrincipal(owner.ID))
					return err
				}
				err = remove(ctx)
				require.NoError(t, err)
				var state string
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT state FROM agents WHERE id=$1`, f.AgentID).Scan(&state))
				var count int
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					`SELECT count(*) FROM event_webhook_deliveries WHERE agent_id=$1`, f.AgentID).Scan(&count))
				require.Equal(t, "archived", state)
				require.Zero(t, count)
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
