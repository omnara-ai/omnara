//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func TestIntegrationInboxLargeSingleMessageFanout(t *testing.T) {
	pool, store, ids, integrationID := integrationWorkerFixture(t)
	ctx := t.Context()
	setup, err := store.Integrations().GetIntegration(ctx, ids.ProjectID, integrationID)
	require.NoError(t, err)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	var agents []uuid.UUID
	for i := range 31 {
		agent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID: ids.ProjectID, AgentConfigID: base.ID,
			LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
		})
		require.NoError(t, err)
		agents = append(agents, agent.Agent.ID)
		address := `{"channel_id":"C123"}`
		if i < 16 {
			address = `{"channel_id":"C123","thread_ts":"1.2"}`
		}
		createTestIntegrationSubscription(t, store, setup, agent.Agent.ID, address)
	}
	createTestIntegrationSubscription(t, store, setup, agents[0], `{"channel_id":"C123"}`)
	text := "large-fanout-once:" + strings.Repeat("x", 100*1024)
	content, err := json.Marshal([]map[string]string{{"type": "text", "text": text}})
	require.NoError(t, err)
	_, _, err = store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "large", Payload: content,
	})
	require.NoError(t, err)
	receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	event := IntegrationEvent{
		Event: integrationdefinition.Event{Kind: integrationdefinition.EventMessage, Scope: integrationdefinition.Scope{
			Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"},
		}},
		SemanticKey: "large-message", ContentBlocks: content, Actor: integrationTestActor(t, setup, "U123"),
	}
	router := NewIntegrationRouter(store.Execution(), store.Integrations())
	plan, _, err := router.Freeze(ctx, receipt.Lease(), &event, nil)
	require.NoError(t, err)
	require.Len(t, plan.Recipients, 31)
	saved, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(saved.Plan, []byte("large-fanout-once:")))
	require.Less(t, len(saved.Plan), 150*1024, "one large shared message plus compact recipient facts")
	results, err := router.Admit(ctx, receipt.Lease(), nil)
	require.NoError(t, err)
	require.Len(t, results, 31)
	for _, result := range results {
		require.True(t, result.Input.Created)
	}
	results, err = router.Admit(ctx, receipt.Lease(), nil)
	require.NoError(t, err)
	for _, result := range results {
		require.False(t, result.Input.Created)
	}
	var count int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE project_id=$1 AND input_idempotency_key=$2`,
		ids.ProjectID, event.SemanticKey,
	).Scan(&count))
	require.Equal(t, 31, count, "independent histories receive the message once, including the overlapping subscriber")
}
