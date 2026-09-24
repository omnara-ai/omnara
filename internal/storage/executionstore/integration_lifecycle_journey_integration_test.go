//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func newIntegrationLifecycleJourney(t *testing.T) integrationInteractionFixture {
	t.Helper()
	f := newIntegrationInteractionFixture(t)
	for _, spec := range []struct {
		integration integrationstore.ProjectIntegrationRecord
		channel     string
	}{
		{f.integration, "C123"}, {f.otherIntegration, "C456"},
	} {
		_, err := f.store.Integrations().CreateIntegrationSubscription(
			f.ctx,
			integrationstore.CreateIntegrationSubscriptionInput{
				OrgID: testOrgID, ProjectID: testProjectID, IntegrationID: spec.integration.ID, AgentID: f.process.AgentID,
				Conversation: json.RawMessage(`{"channel_id":"` + spec.channel + `"}`),
			},
		)
		require.NoError(t, err)
	}
	require.Len(t, f.activation().subscriptions(t, f.process.AgentID), 2)
	selected := f.selectOrigin(t, f.a.ID)
	require.Equal(t, "chat", selected.HandlerKey)
	require.Equal(t, f.a.ID, selected.IntegrationTargetID)
	return f
}

func TestIntegrationDeletionClearsInteractionSelectionAndReleasesCredentials(t *testing.T) {
	t.Parallel()
	f := newIntegrationLifecycleJourney(t)
	question := f.question(t)
	credential := secretstore.DeleteSecretInput{
		OrgID: testOrgID, SecretID: f.integration.CredentialSecretID, Actor: userPrincipal(f.user.ID),
	}
	_, err := f.store.Secrets().DeleteSecret(f.ctx, credential)
	require.ErrorIs(t, err, storeerr.ErrConflict, "the live integration protects its credential")

	require.NoError(t, f.store.Integrations().DeleteProjectIntegration(f.ctx, testOrgID, testProjectID, f.integration.ID))
	selection, err := f.store.Execution().GetInteractionSelection(f.ctx, testProjectID, f.process.AgentID)
	require.NoError(t, err)
	require.Equal(t, executionstore.InteractionSelection{}, selection, "delete clears target and handler together")
	_, err = f.store.Integrations().GetIntegrationTarget(f.ctx, testProjectID, f.a.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.Integrations().GetProjectIntegration(f.ctx, testProjectID, f.integration.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, _, err = f.store.Integrations().AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: testProjectID, IntegrationID: f.integration.ID, ReceiptKey: "after-delete", Payload: []byte(`{}`),
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)

	_, err = f.store.Secrets().DeleteSecret(f.ctx, credential)
	require.NoError(t, err, "the tombstone no longer holds the credential reference")
	var versions int
	require.NoError(t, f.store.pool.QueryRow(f.ctx,
		`SELECT count(*) FROM secret_versions WHERE secret_id=$1`, credential.SecretID).Scan(&versions))
	require.Zero(t, versions, "unreferenced credential ciphertext can be destroyed")

	other, err := f.store.Integrations().GetProjectIntegration(f.ctx, testProjectID, f.otherIntegration.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.ProjectIntegrationStateActive, other.State)
	_, err = f.store.Integrations().GetIntegrationTarget(f.ctx, testProjectID, f.b.ID)
	require.NoError(t, err)
	credential.SecretID = f.otherIntegration.CredentialSecretID
	_, err = f.store.Secrets().DeleteSecret(f.ctx, credential)
	require.ErrorIs(t, err, storeerr.ErrConflict)
	require.Equal(t, "other", f.selectOrigin(t, f.b.ID).HandlerKey)

	retained := f.read(t, question.ID)
	require.Equal(t, executionstore.AgentInteractionStateOpen, retained.State)
	require.JSONEq(t, string(question.Destination), string(retained.Destination))
	input, _, created, err := f.store.Execution().CreateAgentContentInput(f.ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: mustOmnaraActorParams(t, f.user.ID),
			ContentBlocks:  json.RawMessage(`[{"type":"text","text":"Continue in the dashboard"}]`),
			IdempotencyKey: "after-integration-delete",
		})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, f.process.AgentID, input.AgentID)
}

func TestScopeTeardownSweepsLiveIntegrationsSubscriptionsTargetsAndCredentials(t *testing.T) {
	t.Parallel()
	for _, scope := range []string{"project", "organization"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			f := newIntegrationLifecycleJourney(t)
			input, _, created, err := f.store.Execution().CreateAgentContentInput(f.ctx,
				executionstore.CreateAgentContentInputInput{
					ProjectID: testProjectID, AgentID: f.process.AgentID, Actor: mustOmnaraActorParams(t, f.user.ID),
					ContentBlocks:  json.RawMessage(`[{"type":"text","text":"Keep accepted history"}]`),
					IdempotencyKey: "before-scope-delete",
				})
			require.NoError(t, err)
			require.True(t, created)
			if scope == "project" {
				_, err = f.store.Organizations().DeleteProject(f.ctx, testOrgID, testProjectID, userPrincipal(f.user.ID))
			} else {
				_, err = f.store.Organizations().DeleteOrganization(f.ctx, testOrgID, userPrincipal(f.user.ID))
			}
			require.NoError(t, err, "teardown must release integration credentials before checking secret references")
			var deletedIntegrations, liveTargets, activeSubscriptions, versions int
			require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT
			 (SELECT count(*) FROM project_integrations WHERE project_id=$1 AND deleted_at IS NOT NULL
			    AND state='disconnected' AND credential_secret_id IS NULL),
			 (SELECT count(*) FROM integration_targets WHERE project_id=$1 AND deleted_at IS NULL),
			 (SELECT count(*) FROM integration_subscriptions WHERE project_id=$1),
			 (SELECT count(*) FROM secret_versions WHERE secret_id IN ($2,$3))`,
				testProjectID, f.integration.CredentialSecretID, f.otherIntegration.CredentialSecretID).
				Scan(&deletedIntegrations, &liveTargets, &activeSubscriptions, &versions))
			require.Equal(t, 2, deletedIntegrations)
			require.Zero(t, liveTargets)
			require.Zero(t, activeSubscriptions)
			require.Zero(t, versions)
			var agentState string
			var retainedInput bool
			require.NoError(t, f.store.pool.QueryRow(f.ctx, `SELECT state,
			 EXISTS (SELECT 1 FROM agent_inputs WHERE id=$2 AND agent_id=$1) FROM agents WHERE id=$1`,
				f.process.AgentID, input.ID).Scan(&agentState, &retainedInput))
			require.Equal(t, string(executionstore.AgentStateArchived), agentState)
			require.True(t, retainedInput, "scope teardown preserves accepted input history")
			_, _, err = f.store.Integrations().AcceptIntegrationReceipt(f.ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: testProjectID, IntegrationID: f.integration.ID, ReceiptKey: "after-teardown", Payload: []byte(`{}`),
			})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
}
