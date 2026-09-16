//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func cronChannelInput(f publicChannelFixture) executionstore.CreateCronTriggerInput {
	return executionstore.CreateCronTriggerInput{
		ProjectID: testProjectID, Name: "Scheduled channel work",
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAgentProfile, ID: f.agent.AgentProfileID,
		},
		CronExpression: "0 9 * * *", Timezone: "UTC", MessageTemplate: "Original message.",
		Enabled: true, IdempotencyKey: "cron-with-channels",
		ChannelBindings: []executionstore.LaunchChannelBinding{{
			ChannelID: f.target.ID, Grants: integrationstore.ChannelGrants{ReadAllowed: true, SendAllowed: true},
			ReplyChannelGrants: &integrationstore.ChannelGrants{ReceiveAllowed: true, SendAllowed: true},
		}},
	}
}

func secondCronChannel(
	t *testing.T, ctx context.Context, f publicChannelFixture,
) integrationstore.IntegrationTargetRecord {
	t.Helper()
	target, err := f.store.Integrations().CreateIntegrationTarget(ctx, integrationstore.CreateIntegrationTargetInput{
		ProjectID: testProjectID, IntegrationInstallID: f.install.ID, ChannelDefinitionID: f.target.ChannelDefinitionID,
		ProviderRef: "second-conversation", ProviderRefKind: "thread",
	})
	require.NoError(t, err)
	return target
}

func TestCronChannelConfigurationIsExplicitAndIdempotent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-configuration")
	second := secondCronChannel(t, ctx, f)
	input := cronChannelInput(f)
	input.ChannelBindings = append(input.ChannelBindings, executionstore.LaunchChannelBinding{
		ChannelID: second.ID, Grants: integrationstore.ChannelGrants{ReadAllowed: true},
	})
	created, err := f.store.Execution().CreateCronTrigger(ctx, input)
	require.NoError(t, err)
	require.True(t, created.Created)
	require.ElementsMatch(t, input.ChannelBindings, created.ChannelBindings)
	input.ChannelBindings[0], input.ChannelBindings[1] = input.ChannelBindings[1], input.ChannelBindings[0]
	replayed, err := f.store.Execution().CreateCronTrigger(ctx, input)
	require.NoError(t, err)
	require.False(t, replayed.Created)
	require.Equal(t, created.ID, replayed.ID, "ordering does not change the configured set")
	input.ChannelBindings[0].Grants.SendAllowed = true
	_, err = f.store.Execution().CreateCronTrigger(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrIdempotencyConflict)

	plain := cronChannelInput(f)
	plain.Name = "Schedule without channels"
	plain.IdempotencyKey = "same-profile-without-channels"
	plain.ChannelBindings = nil
	unbound, err := f.store.Execution().CreateCronTrigger(ctx, plain)
	require.NoError(t, err)
	require.Empty(t, unbound.ChannelBindings, "a profile does not confer schedule-wide authority")
	listed, err := f.store.Execution().ListCronTriggersForProject(ctx, executionstore.ListCronTriggersForProjectInput{
		ProjectID: testProjectID, Limit: 10,
	})
	require.NoError(t, err)
	require.Len(t, listed.Triggers, 2)
	for _, row := range listed.Triggers {
		if row.ID == created.ID {
			require.Equal(t, created.ChannelBindings, row.ChannelBindings)
		} else {
			require.Empty(t, row.ChannelBindings)
		}
	}
	message := "Changed message only."
	updated, err := f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, MessageTemplate: &message,
	})
	require.NoError(t, err)
	require.Equal(t, created.ChannelBindings, updated.ChannelBindings)
	empty := []executionstore.LaunchChannelBinding{}
	updated, err = f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, ChannelBindings: &empty,
	})
	require.NoError(t, err)
	require.Empty(t, updated.ChannelBindings)
	loaded, err := f.store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
	require.NoError(t, err)
	require.Empty(t, loaded.ChannelBindings)
	var bindings int
	require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_target_bindings`).Scan(&bindings))
	require.Zero(t, bindings, "configuration alone never grants an agent access")
}

func TestCronChannelConfigurationRejectsInvalidGrants(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*executionstore.CreateCronTriggerInput)
	}{
		{"duplicate", func(in *executionstore.CreateCronTriggerInput) {
			in.ChannelBindings = append(in.ChannelBindings, in.ChannelBindings[0])
		}},
		{"empty grant", func(in *executionstore.CreateCronTriggerInput) {
			in.ChannelBindings[0].Grants = integrationstore.ChannelGrants{}
		}},
		{"reply without send", func(in *executionstore.CreateCronTriggerInput) {
			in.ChannelBindings[0].Grants.SendAllowed = false
		}},
		{"empty reply grant", func(in *executionstore.CreateCronTriggerInput) {
			in.ChannelBindings[0].ReplyChannelGrants = &integrationstore.ChannelGrants{}
		}},
		{"nil channel", func(in *executionstore.CreateCronTriggerInput) { in.ChannelBindings[0].ChannelID = uuid.Nil }},
		{"too many", func(in *executionstore.CreateCronTriggerInput) {
			in.ChannelBindings = make([]executionstore.LaunchChannelBinding, executionstore.MaxLaunchChannelBindings+1)
		}},
		{"agent target", func(in *executionstore.CreateCronTriggerInput) {
			in.Target.Kind = executionstore.CronTriggerTargetAgent
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			f := newPublicChannelFixture(t, ctx, "cron-invalid")
			input := cronChannelInput(f)
			tc.edit(&input)
			_, err := f.store.Execution().CreateCronTrigger(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			var count int
			require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT count(*) FROM cron_triggers`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestCronChannelConfigurationChecksProjectAndLiveAuthority(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newPublicChannelFixture(t, ctx, "cron-live-authority")
	input := cronChannelInput(f)
	created, err := f.store.Execution().CreateCronTrigger(ctx, input)
	require.NoError(t, err)
	otherProject := seedAdditionalProjectForTest(t, ctx, f.store.pool, "cron-foreign")
	connectionInput := externalConnectionInput(f.user.ID)
	connectionInput.ProjectID = otherProject
	foreignInstall, err := f.store.Integrations().CreateExternalIntegrationInstall(ctx, connectionInput)
	require.NoError(t, err)
	definitionInput := externalDefinitionInput(foreignInstall.ID)
	definitionInput.ProjectID = otherProject
	foreignDefinition, err := f.store.Integrations().PublishExternalChannelDefinition(ctx, definitionInput)
	require.NoError(t, err)
	foreignChannel, err := f.store.Integrations().CreateIntegrationTarget(ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID: otherProject, IntegrationInstallID: foreignInstall.ID, ChannelDefinitionID: foreignDefinition.ID,
			ProviderRef: "foreign-conversation", ProviderRefKind: "thread",
		})
	require.NoError(t, err)
	wrong := []executionstore.LaunchChannelBinding{{
		ChannelID: foreignChannel.ID, Grants: integrationstore.ChannelGrants{SendAllowed: true},
	}}
	changedMessage := "Must roll back."
	_, err = f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, ChannelBindings: &wrong, MessageTemplate: &changedMessage,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	loaded, err := f.store.Execution().GetCronTrigger(ctx, testProjectID, created.ID)
	require.NoError(t, err)
	require.Equal(t, created.MessageTemplate, loaded.MessageTemplate)
	require.Equal(t, created.ChannelBindings, loaded.ChannelBindings)
	_, err = f.store.pool.Exec(ctx, `INSERT INTO cron_trigger_channel_bindings
(project_id,cron_trigger_id,channel_id,receive_allowed,read_allowed,send_allowed)
VALUES ($1,$2,$3,false,false,true)`, uuid.New(), created.ID, f.target.ID)
	require.Error(t, err, "composite foreign keys reject a channel grant outside its trigger project")

	require.NoError(t, f.store.Integrations().DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
	input.IdempotencyKey = "retired-connection"
	_, err = f.store.Execution().CreateCronTrigger(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	enabled := false
	_, err = f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, Enabled: &enabled,
	})
	require.NoError(t, err, "retired channels must not prevent disabling the schedule")
	enabled = true
	_, err = f.store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID, TriggerID: created.ID, Enabled: &enabled,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}
