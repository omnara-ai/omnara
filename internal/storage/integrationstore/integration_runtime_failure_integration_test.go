//go:build integration

package integrationstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/require"
)

func TestAgentIntegrationLaunchOwnerLookup(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"active", "attribution only", "unassigned", "reassigned", "other project", "other agent", "two integrations",
		"target deleted",
		"integration disconnected", "integration deleted", "agent archived", "project deleted", "org deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newAgentConversationFixture(t)
			input := integrationstore.EnsureConversationTargetInput{
				ProjectID: f.project, AgentID: f.agentID, IntegrationID: f.integrationID,
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}, LaunchKey: "default",
			}
			if scenario == "attribution only" {
				input.LaunchKey = ""
			}
			target, err := f.ensureConversationTarget(input)
			require.NoError(t, err)
			if scenario != "unassigned" {
				require.NoError(t, f.assign(input.IntegrationID, input.Address))
			}
			project, agent := f.project, f.agentID
			switch scenario {
			case "reassigned":
				input.Address.Ref = "C123:3.4"
				target, err = f.ensureConversationTarget(input)
				require.NoError(t, err)
				require.NoError(t, f.assign(input.IntegrationID, input.Address))
			case "other project":
				project = uuid.New()
			case "other agent":
				agent = uuid.New()
			case "two integrations":
				input.IntegrationID = f.addIntegration(t, "another-launch", nil).ID
				_, err := f.ensureConversationTarget(input)
				require.NoError(t, err)
				require.NoError(t, f.assign(input.IntegrationID, input.Address))
			case "target deleted":
				f.exec(t, `UPDATE integration_targets SET deleted_at=now() WHERE id=$1`, target.ID)
			case "integration disconnected":
				f.exec(t, `UPDATE integrations SET state='disconnected' WHERE id=$1`, f.integrationID)
			case "integration deleted":
				f.exec(t, `UPDATE integrations SET deleted_at=now() WHERE id=$1`, f.integrationID)
			case "agent archived":
				_, _, err := executionstore.New(f.pool, executionstore.Config{}).ArchiveAgent(
					f.ctx, f.project, f.agentID, identitystore.NewUserPrincipal(f.user),
				)
				require.NoError(t, err)
			case "project deleted":
				f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
			case "org deleted":
				f.exec(t, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.org)
			}
			owners, err := f.store.ListAgentIntegrationLaunchOwners(f.ctx, project, agent)
			require.NoError(t, err)
			switch scenario {
			case "active", "reassigned":
				require.Len(t, owners, 1)
				require.Equal(t, target.ID, owners[0].ID)
				require.Equal(t, target.AgentID, owners[0].AgentID)
				require.Equal(t, target.LaunchKey, owners[0].LaunchKey)
			case "two integrations":
				require.Len(t, owners, 2, "each integration's assigned conversation is a destination")
			default:
				require.Empty(t, owners)
			}
		})
	}
}
