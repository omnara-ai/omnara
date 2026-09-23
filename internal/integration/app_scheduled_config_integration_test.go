//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

func (f *scheduledJourney) retargetProfile(instruction string) executionstore.AgentConfigRecord {
	f.t.Helper()
	config := storagefixture.SeedAgentConfig(f.t, f.t.Context(), f.store.Models(), f.store.Execution(),
		f.ids.OrgID, f.ids.ProjectID,
		"instruction: "+instruction+"\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
	)
	profile, err := f.store.Execution().GetAgentProfile(f.t.Context(), f.ids.ProjectID, f.profile.ID)
	require.NoError(f.t, err)
	_, err = f.store.Execution().RetargetAgentProfile(f.t.Context(), executionstore.RetargetAgentProfileInput{
		ProjectID: f.ids.ProjectID, ProfileID: f.profile.ID,
		ExpectedCurrentConfigID: profile.CurrentConfigID, ConfigID: config.ID,
	})
	require.NoError(f.t, err)
	return config
}

func TestScheduledLaunchPinsCurrentConfigBeforePublication(t *testing.T) {
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		for _, duringPublication := range []bool{false, true} {
			phase := "after cron handoff"
			if duringPublication {
				phase = "during publication"
			}
			t.Run(provider+"/"+phase, func(t *testing.T) {
				f := newScheduledProviderJourney(t, provider)
				receipt := f.fire()
				var edited executionstore.AgentConfigRecord
				edit := func(context.Context) error {
					saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
					require.NoError(t, err)
					require.Empty(t, saved.Plan)
					edited = f.retargetProfile("Use the config selected before plan construction")
					return nil
				}
				if duringPublication {
					f.provider.publish = edit
				} else {
					require.NoError(t, edit(t.Context()))
				}
				results, err := f.consumer.Consume(t.Context(), receipt.Lease())
				require.NoError(t, err)
				require.Len(t, results, 1)
				require.True(t, results[0].Launch.Created)
				saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
				require.NoError(t, err)
				plan, err := decodeAppInboxPlan(saved.Plan)
				require.NoError(t, err)
				require.NotEqual(t, f.profile.CurrentConfigID, edited.ID)
				expected := edited.ID
				if duringPublication {
					expected = f.profile.CurrentConfigID
				}
				require.Equal(t, expected, plan["scheduled"].Launch.DerivedBaseConfigID)
				config, found, err := f.store.Execution().GetAgentConfig(t.Context(), f.ids.ProjectID,
					results[0].Launch.Agent.CurrentConfigID)
				require.NoError(t, err)
				require.True(t, found)
				if duringPublication {
					require.NotContains(t, string(config.CompiledDefinition), "Use the config selected before plan construction")
				} else {
					require.Contains(t, string(config.CompiledDefinition), "Use the config selected before plan construction")
				}
				require.Equal(t, 1, f.provider.posts)
			})
		}
	}
}

func TestScheduledLaunchRetryReusesConfigFrozenBeforeProfileEdit(t *testing.T) {
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			f := newScheduledProviderJourney(t, provider)
			receipt := f.fire()
			before := f.retargetProfile("Use the frozen profile config")
			f.provider.ensure = func(context.Context) error { return errors.New("thread preparation unavailable") }
			worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
			require.ErrorContains(t, worker.consume(t.Context(), receipt), "thread preparation unavailable")
			frozen, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxPending, frozen.State)
			plan, err := decodeAppInboxPlan(frozen.Plan)
			require.NoError(t, err)
			require.Equal(t, before.ID, plan["scheduled"].Launch.DerivedBaseConfigID)
			after := f.retargetProfile("This later edit must not change the frozen launch")
			require.NotEqual(t, before.ID, after.ID)
			f.provider.ensure = nil
			_, err = f.pool.Exec(t.Context(),
				`UPDATE integration_inbox SET available_at=now()-interval '1 second' WHERE id=$1`, receipt.ID)
			require.NoError(t, err)
			resumed := f.claim()
			require.Equal(t, receipt.ID, resumed.ID)
			require.NotEqual(t, receipt.ClaimToken, resumed.ClaimToken)
			results, err := f.consumer.Consume(t.Context(), resumed.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.Equal(t, plan["scheduled"].AgentID, results[0].Launch.Agent.ID)
			config, found, err := f.store.Execution().GetAgentConfig(t.Context(), f.ids.ProjectID,
				results[0].Launch.Agent.CurrentConfigID)
			require.NoError(t, err)
			require.True(t, found)
			require.Contains(t, string(config.CompiledDefinition), "Use the frozen profile config")
			require.NotContains(t, string(config.CompiledDefinition), "This later edit")
			completed, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
			require.JSONEq(t, string(frozen.Plan), string(completed.Plan))
			require.Equal(t, 1, f.provider.posts)
			require.Equal(t, 2, f.provider.ensures)
		})
	}
}

func TestScheduledLaunchRejectsDeletedProfileBeforePublication(t *testing.T) {
	f := newScheduledJourney(t)
	receipt := f.fire()
	require.NoError(t, f.store.Execution().DeleteAgentProfile(t.Context(), f.ids.ProjectID, f.profile.ID))
	worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
	err := worker.consume(t.Context(), receipt)
	require.ErrorContains(t, err, "load agent profile")
	require.Zero(t, f.provider.posts)
	require.Zero(t, f.provider.ensures)
	saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxFailed, saved.State)
	require.NotEmpty(t, saved.LastError, "resource unavailability remains visible on the accepted occurrence")
	require.Empty(t, saved.Plan)
	_, err = f.store.Execution().GetCronTrigger(t.Context(), f.ids.ProjectID, f.trigger.ID)
	require.NoError(t, err, "deleting an app-owned profile input must not delete the schedule")
}

func TestScheduledLaunchRejectsForeignProjectProfileBeforePublication(t *testing.T) {
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			f := newScheduledProviderJourney(t, provider)
			foreignProject := uuid.New()
			storagefixture.InsertProject(t, t.Context(), f.pool, f.ids.OrgID, foreignProject,
				"Another project", "another-project", time.Now())
			config := storagefixture.SeedAgentConfig(t, t.Context(), f.store.Models(), f.store.Execution(),
				f.ids.OrgID, foreignProject,
				"instruction: Another project\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n",
			)
			profile, err := f.store.Execution().CreateAgentProfile(t.Context(), executionstore.CreateAgentProfileInput{
				ProjectID: foreignProject, Name: "foreign", CurrentConfigID: config.ID,
			})
			require.NoError(t, err)
			var settings appdefinition.ThreadScheduleSettings
			require.NoError(t, json.Unmarshal(f.trigger.Target.Settings, &settings))
			settings.AgentProfileID, err = publicid.Encode(publicid.KindAgentProfile, profile.ID)
			require.NoError(t, err)
			target := f.trigger.Target
			target.Settings, err = json.Marshal(settings)
			require.NoError(t, err)
			_, err = f.store.Execution().UpdateCronTrigger(t.Context(), executionstore.UpdateCronTriggerInput{
				ProjectID: f.ids.ProjectID, TriggerID: f.trigger.ID, Target: &target,
			})
			require.NoError(t, err, "the app settings schema permits a syntactically valid profile ID")
			receipt := f.fire()
			worker := NewAppInboxWorker(f.store.Integrations(), f.consumer, AppInboxWorkerOptions{})
			require.ErrorContains(t, worker.consume(t.Context(), receipt), "load agent profile")
			require.Zero(t, f.provider.posts, "project-scoped profile lookup must precede provider I/O")
			require.Zero(t, f.provider.ensures)
			saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxFailed, saved.State)
			require.NotEmpty(t, saved.LastError)
			require.Empty(t, saved.Plan)
			var agents int
			require.NoError(t, f.pool.QueryRow(t.Context(),
				`SELECT count(*) FROM agents WHERE project_id=$1`, f.ids.ProjectID).Scan(&agents))
			require.Zero(t, agents)
		})
	}
}

func TestScheduledLaunchConfigFailurePrecedesPublication(t *testing.T) {
	for _, provider := range []string{appdefinition.ProviderSlack, appdefinition.ProviderDiscord} {
		t.Run(provider, func(t *testing.T) {
			f := newScheduledProviderJourney(t, provider)
			receipt := f.fire()
			_, err := f.pool.Exec(t.Context(),
				`INSERT INTO org_resource_limit_overrides(org_id,max_agent_configs_per_project) VALUES($1,1)`, f.ids.OrgID,
			)
			require.NoError(t, err)
			_, err = f.consumer.Consume(t.Context(), receipt.Lease())
			require.ErrorContains(t, err, "agent configs")
			require.Zero(t, f.provider.posts)
			require.Zero(t, f.provider.ensures)
			saved, err := f.store.Integrations().GetIntegrationInbox(t.Context(), f.ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Empty(t, saved.Plan)
			_, err = f.pool.Exec(t.Context(), `DELETE FROM org_resource_limit_overrides WHERE org_id=$1`, f.ids.OrgID)
			require.NoError(t, err)
			results, err := f.consumer.Consume(t.Context(), receipt.Lease())
			require.NoError(t, err)
			require.Len(t, results, 1)
			require.True(t, results[0].Launch.Created)
			require.Equal(t, 1, f.provider.posts)
		})
	}
}
