//go:build integration

package executionstore_test

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookScopeFanoutAndLifecycle(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	store := f.store.Execution()
	settings := webhookconfig.Settings{URL: "https://example.com/events", EventTypes: []string{"tool_call_update"}}
	otherProject, err := f.store.Identity().CreateProjectForPrincipal(f.ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: userPrincipal(f.user.ID), Name: "Other Webhook Project",
	})
	require.NoError(t, err)
	otherWebhook, err := store.CreateEventWebhook(f.ctx, executionstore.CreateEventWebhookInput{
		ProjectID: otherProject.ID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeProject,
		},
		Settings: settings,
	})
	require.NoError(t, err)
	var webhooks []executionstore.EventWebhookRecord
	for _, scope := range []executionstore.EventWebhookScope{
		{Kind: executionstore.EventWebhookScopeProject},
		{Kind: executionstore.EventWebhookScopeAgentProfile, ID: f.profile.ID},
	} {
		webhook, err := store.CreateEventWebhook(f.ctx, executionstore.CreateEventWebhookInput{
			ProjectID: testProjectID, Scope: scope,
			Settings: settings})
		require.NoError(t, err)
		webhooks = append(webhooks, webhook)
	}
	launch := executionstore.LaunchAgentInput{ProjectID: testProjectID, ProfileID: f.profile.ID,
		AgentConfigID: f.profile.CurrentConfigID, LaunchedBy: userPrincipal(f.user.ID)}
	first, err := store.LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	for _, url := range []string{settings.URL, "https://example.com/other"} {
		_, err := store.CreateEventWebhook(f.ctx, executionstore.CreateEventWebhookInput{
			ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
				Kind: executionstore.EventWebhookScopeAgent,
				ID:   first.Agent.ID,
			},
			Settings: webhookconfig.Settings{URL: url, EventTypes: settings.EventTypes},
		})
		require.NoError(t, err)
	}
	otherEndpoint, err := store.GetEventWebhookEndpoint(f.ctx, testOrgID, otherWebhook.ID, first.Agent.ID)
	require.NoError(t, err)
	require.Empty(t, otherEndpoint.URL)
	own, err := store.ListEventWebhooks(f.ctx, testProjectID, executionstore.EventWebhookScope{
		Kind: executionstore.EventWebhookScopeAgent,
		ID:   first.Agent.ID,
	})
	require.NoError(t, err)
	require.Len(t, own, 2)
	second, err := store.LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	launch.ProfileID = uuid.Nil
	third, err := store.LaunchAgent(f.ctx, launch)
	require.NoError(t, err)

	for _, test := range []struct {
		agentID    uuid.UUID
		webhookIDs []uuid.UUID
	}{
		{first.Agent.ID, []uuid.UUID{webhooks[0].ID, webhooks[1].ID, own[0].ID, own[1].ID}},
		{second.Agent.ID, []uuid.UUID{webhooks[0].ID, webhooks[1].ID}},
		{third.Agent.ID, []uuid.UUID{webhooks[0].ID}},
	} {
		toolID := uuid.New()
		pending := notifications.NewTxNotifications()
		pending.AddToolCallUpdate(test.agentID, toolID, "ready", nil)
		unit, err := store.IntegrationBeginUnit(f.ctx)
		require.NoError(t, err)
		defer func() { _ = unit.Rollback(f.ctx) }()
		*unit.Notifications() = *pending
		require.NoError(t, unit.Commit(f.ctx, "webhook scope"))
		var ids []uuid.UUID
		require.NoError(t, f.store.pool.QueryRow(f.ctx,
			`SELECT array_agg(webhook_id) FROM event_webhook_deliveries WHERE tool_call_id = $1`,
			toolID).Scan(&ids))
		require.ElementsMatch(t, test.webhookIDs, ids)
	}

	settings.URL = "https://example.com/updated"
	settings.EventTypes = []string{"model_output"}
	updated, err := store.UpdateEventWebhook(f.ctx, executionstore.UpdateEventWebhookInput{
		ProjectID:  testProjectID,
		WebhookID:  webhooks[1].ID,
		URL:        &settings.URL,
		EventTypes: settings.EventTypes,
	})
	require.NoError(t, err)
	require.Equal(t, webhooks[1].Scope, updated.Scope)
	endpoint, err := store.GetEventWebhookEndpoint(f.ctx, testOrgID, webhooks[1].ID, first.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, testProjectID, endpoint.ProjectID)
	require.Equal(t, settings.URL, endpoint.URL)
	pending := notifications.NewTxNotifications()
	filteredToolID := uuid.New()
	pending.AddToolCallUpdate(first.Agent.ID, filteredToolID, "ready", nil)
	unit, err := store.IntegrationBeginUnit(f.ctx)
	require.NoError(t, err)
	defer func() { _ = unit.Rollback(f.ctx) }()
	*unit.Notifications() = *pending
	require.NoError(t, unit.Commit(f.ctx, "updated filters"))
	var count int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM event_webhook_deliveries WHERE webhook_id = $1 AND tool_call_id = $2`,
		webhooks[1].ID, filteredToolID).Scan(&count))
	require.Zero(t, count)

	require.NoError(t, store.DeleteAgentProfile(f.ctx, testProjectID, f.profile.ID))
	endpoint, err = store.GetEventWebhookEndpoint(f.ctx, testOrgID, webhooks[1].ID, first.Agent.ID)
	require.NoError(t, err)
	require.Empty(t, endpoint.URL)
	agent, err := store.GetAgentInProject(f.ctx, testProjectID, first.Agent.ID)
	require.NoError(t, err)
	require.Equal(t, executionstore.AgentStateActive, agent.State)
	require.NoError(t, store.DeleteEventWebhook(f.ctx, testProjectID, own[0].ID))
	endpoint, err = store.GetEventWebhookEndpoint(f.ctx, testOrgID, own[0].ID, first.Agent.ID)
	require.NoError(t, err)
	require.Empty(t, endpoint.URL)
	endpoint, err = store.GetEventWebhookEndpoint(f.ctx, testOrgID, own[1].ID, first.Agent.ID)
	require.NoError(t, err)
	require.NotEmpty(t, endpoint.URL)
}

func TestEventWebhookEventTypesConstraint(t *testing.T) {
	t.Parallel()
	fixture := newProcessDaemonFixture(t, t.Context(), "webhook_event_types")
	webhook := createAgentEventWebhook(t, fixture, nil)
	for _, test := range []struct {
		name       string
		eventTypes string
	}{
		{"unknown", "ARRAY['unknown']"},
		{"mixed", "ARRAY['tool_call_update', 'unknown']"},
		{"empty", "ARRAY[]::text[]"},
		{"null_element", "ARRAY['tool_call_update', NULL]"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := fixture.Store.pool.Exec(t.Context(),
				"UPDATE event_webhooks SET event_types = "+test.eventTypes+" WHERE id = $1", webhook.ID)
			assertPgConstraint(t, err, "23514", "event_webhooks_event_types_check")
		})
	}
}

func TestEventWebhookScopeValidationAndLimits(t *testing.T) {
	t.Parallel()
	fixture := newProcessDaemonFixture(t, t.Context(), "webhook_scope")
	store := fixture.Store.Execution()
	input := executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgent,
			ID:   fixture.AgentID,
		},
		Settings: webhookconfig.Settings{URL: "https://example.com/events",
			EventTypes: []string{"tool_call_update"}},
	}
	first, err := store.CreateEventWebhook(t.Context(), input)
	require.NoError(t, err)
	for i := range executionstore.MaxEventWebhooksPerScope - 1 {
		input.Settings.URL = fmt.Sprintf("https://example.com/events/%d", i)
		_, err := store.CreateEventWebhook(t.Context(), input)
		require.NoError(t, err)
	}
	input.Settings.URL = "https://example.com/over-limit"
	_, err = store.CreateEventWebhook(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.NoError(t, store.DeleteEventWebhook(t.Context(), testProjectID, first.ID))
	input.Settings.URL = first.Settings.URL
	recreated, err := store.CreateEventWebhook(t.Context(), input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, recreated.ID)
	_, err = store.GetEventWebhook(t.Context(), uuid.New(), recreated.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	input.ProjectID = uuid.New()
	_, err = store.CreateEventWebhook(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	input.ProjectID = testProjectID
	input.Scope.Kind = "org"
	input.Scope.ID = uuid.Nil
	_, err = store.CreateEventWebhook(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	input.Scope = executionstore.EventWebhookScope{
		Kind: executionstore.EventWebhookScopeProject,
		ID:   fixture.AgentID,
	}
	_, err = store.CreateEventWebhook(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
}

func TestEventWebhookUniqueURLsPerScope(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	store := f.store.Execution()
	otherProject, err := f.store.Identity().CreateProjectForPrincipal(f.ctx, identitystore.CreateProjectForPrincipalInput{
		OrgID: testOrgID, Creator: userPrincipal(f.user.ID), Name: "Other Webhook Project",
	})
	require.NoError(t, err)
	otherProfile, err := store.CreateAgentProfile(f.ctx, executionstore.CreateAgentProfileInput{
		ProjectID: testProjectID, Name: "Other Webhook Profile", CurrentConfigID: f.profile.CurrentConfigID,
	})
	require.NoError(t, err)
	launch := executionstore.LaunchAgentInput{ProjectID: testProjectID, ProfileID: f.profile.ID,
		AgentConfigID: f.profile.CurrentConfigID, LaunchedBy: userPrincipal(f.user.ID)}
	firstAgent, err := store.LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	secondAgent, err := store.LaunchAgent(f.ctx, launch)
	require.NoError(t, err)
	for _, input := range []executionstore.CreateEventWebhookInput{
		{ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeProject,
		}},
		{ProjectID: otherProject.ID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeProject,
		}},
		{ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgentProfile,
			ID:   f.profile.ID,
		}},
		{ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgentProfile,
			ID:   otherProfile.ID,
		}},
		{ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgent,
			ID:   firstAgent.Agent.ID,
		}},
		{ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgent,
			ID:   secondAgent.Agent.ID,
		}},
	} {
		input.Settings = webhookconfig.Settings{URL: "https://example.com/events", EventTypes: []string{"tool_call_update"}}
		first, err := store.CreateEventWebhook(f.ctx, input)
		require.NoError(t, err)
		_, err = store.CreateEventWebhook(f.ctx, input)
		require.ErrorIs(t, err, storeerr.ErrConflict)
		input.Settings.EventTypes = []string{"model_output"}
		_, err = store.CreateEventWebhook(f.ctx, input)
		require.ErrorIs(t, err, storeerr.ErrConflict)
		_, err = store.UpdateEventWebhook(f.ctx, executionstore.UpdateEventWebhookInput{
			ProjectID:  input.ProjectID,
			WebhookID:  first.ID,
			EventTypes: input.Settings.EventTypes,
		})
		require.NoError(t, err)
		input.Settings.URL = "https://example.com/other"
		second, err := store.CreateEventWebhook(f.ctx, input)
		require.NoError(t, err)
		_, err = store.UpdateEventWebhook(f.ctx, executionstore.UpdateEventWebhookInput{
			ProjectID: input.ProjectID,
			WebhookID: first.ID,
			URL:       &second.Settings.URL,
		})
		require.ErrorIs(t, err, storeerr.ErrConflict)
		unchanged, err := store.GetEventWebhook(f.ctx, input.ProjectID, first.ID)
		require.NoError(t, err)
		require.Equal(t, first.Settings.URL, unchanged.Settings.URL)
		require.NoError(t, store.DeleteEventWebhook(f.ctx, input.ProjectID, second.ID))
		_, err = store.UpdateEventWebhook(f.ctx, executionstore.UpdateEventWebhookInput{
			ProjectID: input.ProjectID,
			WebhookID: first.ID,
			URL:       &second.Settings.URL,
		})
		require.NoError(t, err)
		input.Settings.URL = first.Settings.URL
		_, err = store.CreateEventWebhook(f.ctx, input)
		require.NoError(t, err)
	}
}

func TestEventWebhookConcurrentURLConflict(t *testing.T) {
	t.Parallel()
	f := newProcessDaemonFixture(t, t.Context(), "webhook_concurrent")
	store := f.Store.Execution()
	input := executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgent,
			ID:   f.AgentID,
		},
		Settings: webhookconfig.Settings{URL: "https://example.com/original", EventTypes: []string{"tool_call_update"}},
	}
	existing, err := store.CreateEventWebhook(t.Context(), input)
	require.NoError(t, err)
	input.Settings.URL = "https://example.com/shared"
	results := make(chan error, 2)
	start := make(chan struct{})
	go func() {
		<-start
		_, err := store.CreateEventWebhook(t.Context(), input)
		results <- err
	}()
	go func() {
		<-start
		_, err := store.UpdateEventWebhook(t.Context(), executionstore.UpdateEventWebhookInput{
			ProjectID: testProjectID,
			WebhookID: existing.ID,
			URL:       &input.Settings.URL,
		})
		results <- err
	}()
	close(start)
	first, second := <-results, <-results
	if first == nil {
		require.ErrorIs(t, second, storeerr.ErrConflict)
	} else {
		require.ErrorIs(t, first, storeerr.ErrConflict)
		require.NoError(t, second)
	}
}

func TestEventWebhookConcurrentPartialUpdates(t *testing.T) {
	t.Parallel()
	f := newProcessDaemonFixture(t, t.Context(), "webhook_patch")
	store := f.Store.Execution()
	webhook, err := store.CreateEventWebhook(t.Context(), executionstore.CreateEventWebhookInput{
		ProjectID: testProjectID, Scope: executionstore.EventWebhookScope{
			Kind: executionstore.EventWebhookScopeAgent,
			ID:   f.AgentID,
		},
		Settings: webhookconfig.Settings{URL: "https://example.com/events", EventTypes: []string{"tool_call_update"}},
	})
	require.NoError(t, err)
	url := "https://example.com/updated"
	results := make(chan error, 2)
	start := make(chan struct{})
	for _, input := range []executionstore.UpdateEventWebhookInput{
		{ProjectID: testProjectID, WebhookID: webhook.ID, URL: &url},
		{ProjectID: testProjectID, WebhookID: webhook.ID, EventTypes: []string{"model_output"}},
	} {
		go func() {
			<-start
			_, err := store.UpdateEventWebhook(t.Context(), input)
			results <- err
		}()
	}
	close(start)
	require.NoError(t, <-results)
	require.NoError(t, <-results)
	updated, err := store.GetEventWebhook(t.Context(), testProjectID, webhook.ID)
	require.NoError(t, err)
	require.Equal(t, url, updated.Settings.URL)
	require.Equal(t, []string{"model_output"}, updated.Settings.EventTypes)
	require.Equal(t, webhook.Scope, updated.Scope)
}

func TestEventWebhookDatabaseRejectsMultipleScopes(t *testing.T) {
	t.Parallel()
	f := newIntegrationActivationFixture(t)
	agent, err := f.store.Execution().LaunchAgent(f.ctx, executionstore.LaunchAgentInput{
		ProjectID: testProjectID, ProfileID: f.profile.ID, AgentConfigID: f.profile.CurrentConfigID,
		LaunchedBy: userPrincipal(f.user.ID),
	})
	require.NoError(t, err)
	_, err = f.store.pool.Exec(f.ctx,
		`INSERT INTO event_webhooks (org_id, project_id, agent_profile_id, agent_id, url, event_types)
		VALUES ($1, $2, $3, $4, 'https://example.com/events', ARRAY['tool_call_update'])`,
		testOrgID, testProjectID, f.profile.ID, agent.Agent.ID)
	require.ErrorContains(t, err, "event_webhooks_scope_check")
}
