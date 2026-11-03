//go:build integration

package integration

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDiscordRoleMentionRetriesThenChoosesOnce(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
	_, err := pool.Exec(ctx, `UPDATE integrations SET provider_config=$2 WHERE id=$1`, integrationID,
		`{"public_key":"`+strings.Repeat("a", 64)+`"}`)
	require.NoError(t, err)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	var profiles []uuid.UUID
	for _, name := range []string{"support", "review"} {
		profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
			ProjectID: ids.ProjectID, Name: name, CurrentConfigID: base.ID,
		})
		require.NoError(t, err)
		profiles = append(profiles, profile.ID)
	}
	integrationSetup, err := store.Integrations().UpdateIntegration(ctx, integrationID,
		integrationstore.SaveIntegrationInput{
			OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationKind: integrationdefinition.DiscordThread,
			Settings: integrationtest.ChatSettings(profiles...),
		})
	require.NoError(t, err)
	f, provider := newDiscordInboxFixture(t)
	f.integrationSetup, provider.integrations = integrationSetup, store.Integrations()
	f.message.Mentions, f.message.MentionRoles, f.message.Content = nil, []string{"700"}, "<@&700> original request"
	providers := map[integrationdefinition.Provider]IntegrationInboxProvider{
		integrationdefinition.ProviderDiscord: provider,
	}
	newConsumer := func() *IntegrationInboxConsumer {
		router := NewIntegrationRouter(store.Execution(), store.Integrations())
		launcher := NewChatIntegrationLauncher(store.Integrations(), store.Execution(), providers)
		workflow := NewIntegrationLaunchWorkflow(router, map[integrationdefinition.Kind]IntegrationLauncher{
			integrationdefinition.DiscordThread: launcher.Decide,
		}, providers)
		return NewIntegrationInboxConsumer(router, store.Integrations(), nil, providers, nil, workflow,
			WithIntegrationStateHandlers(map[integrationdefinition.Kind]IntegrationStateHandler{
				integrationdefinition.DiscordThread: launcher.HandleState,
			}))
	}
	claim := func() integrationstore.IntegrationInboxRecord {
		t.Helper()
		receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
			ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
		})
		require.NoError(t, err)
		require.True(t, found)
		return receipt
	}
	raw := discordInboxPayload(t, f.message)
	_, _, err = store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "discord:500", Payload: raw,
	})
	require.NoError(t, err)
	receipt := claim()
	rateLimited, menuPosts := true, 0
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v10/guilds/100/roles" && rateLimited {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":10}`))
			return true
		}
		if r.Method != http.MethodPost || r.URL.Path != "/api/v10/channels/500/messages" {
			return false
		}
		menuPosts++
		var body struct {
			Nonce      json.RawMessage     `json:"nonce"`
			Components []discord.ActionRow `json:"components"`
		}
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		if assert.Len(t, body.Components, 1) && assert.Len(t, body.Components[0].Components, 1) {
			assert.Len(t, body.Components[0].Components[0].Options, 2)
		}
		assert.NoError(t, json.NewEncoder(w).Encode(discord.Message{
			ID: "900", ChannelID: "500", Author: discord.User{ID: "22"}, Nonce: body.Nonce,
		}))
		return true
	}
	consumer := newConsumer()
	_, err = consumer.Consume(ctx, receipt.Lease())
	var apiErr *discord.APIError
	require.ErrorAs(t, err, &apiErr)
	require.Equal(t, discord.RateLimited, apiErr.Code)
	latest, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Empty(t, latest.Plan, "role lookup failure must not freeze an irrelevant input")
	f.mu.Lock()
	rateLimited = false
	f.mu.Unlock()
	results, err := consumer.Consume(ctx, receipt.Lease())
	require.NoError(t, err)
	require.Empty(t, results)
	choice, found, err := store.Integrations().GetIntegrationProfileChoiceBySource(
		ctx, ids.ProjectID, integrationID, "discord:message:11:500")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "900", choice.MessageID)
	require.Equal(t, "500", choice.MessageChannelID)
	var source IntegrationEvent
	require.NoError(t, json.Unmarshal(choice.Event, &source))
	require.True(t, source.Event.Mentioned)
	for range 2 {
		_, err = SelectChatIntegrationProfile(ctx, store.Integrations(), integrationSetup,
			choice.ID, choice.Options[1].Key, "33", "500", "900")
		require.NoError(t, err)
	}
	f.mu.Lock()
	f.roles = json.RawMessage(`[]`)
	f.mu.Unlock()
	consumer = newConsumer()
	selected := claim()
	require.Equal(t, integrationstore.IntegrationInboxSourceState, selected.Source)
	results, err = consumer.Consume(ctx, selected.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, profiles[1], results[0].Launch.Agent.AgentProfileID)
	var content string
	require.NoError(t, pool.QueryRow(ctx, `SELECT text_content FROM content_blocks
		WHERE owner_agent_input_id=$1 AND ordinal=0`, results[0].Launch.AgentInput.ID).Scan(&content))
	require.Equal(t, f.message.Content, content)
	results, err = consumer.Consume(ctx, selected.Lease())
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.False(t, results[0].Launch.Created)
	f.mu.Lock()
	defer f.mu.Unlock()
	require.Equal(t, 1, menuPosts)
	require.Equal(t, 1, f.posts)
}
