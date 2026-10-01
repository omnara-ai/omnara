//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestIntegrationSubscriptionLaunchBatchRejectsInvalidAttachment(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"wrong provider",
		"invalid address",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationActivationFixture(t)
			f.integration = inboxInputIntegration(t, f, "github")
			first := integrationstore.IntegrationSubscriptionAttachment{
				IntegrationID: f.integration.ID, Conversation: json.RawMessage(`{"repository_id":123,"pull_request":42}`),
			}
			second := first
			switch scenario {
			case "wrong provider":
				second.Conversation = json.RawMessage(`{"channel_id":"C123"}`)
			case "invalid address":
				second.Conversation = json.RawMessage(`{"repository_id":123}`)
			}
			definition := f.definition(t, "Invalid attachment batch")
			input := f.launchInput(uuid.Nil, "invalid-batch")
			input.DerivedConfig = &definition
			input.Subscriptions = []integrationstore.IntegrationSubscriptionAttachment{first, second}
			input.Message = "Must not be admitted"
			_, err := f.store.Execution().LaunchAgent(f.ctx, input)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			var agents, configs, subscriptions int
			require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT
				(SELECT count(*) FROM agents WHERE project_id=$1 AND idempotency_key=$2),
				(SELECT count(*) FROM agent_configs WHERE project_id=$1 AND effective_definition_hash=$3),
				(SELECT count(*) FROM integration_subscriptions WHERE project_id=$1)`,
				testProjectID, input.IdempotencyKey, definition.EffectiveDefinitionHash).Scan(&agents, &configs, &subscriptions))
			require.Zero(t, agents)
			require.Zero(t, configs)
			require.Zero(t, subscriptions)
		})
	}
}
