//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func grantCallbackCredential(
	t *testing.T, project publicHTTPProject, integration integrationstore.IntegrationRecord,
) secretstore.DeleteSecretGrantInput {
	t.Helper()
	actor := httpUserPrincipal(project.AdminUserUUID)
	secret, version, err := project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
		OrgID: project.OrgUUID, OwnerKind: secretstore.SecretOwnerOrg, Name: "shared-callback", Actor: actor,
		Material: secrets.GenericMaterial{Value: "test-bot-token"},
	})
	require.NoError(t, err)
	grant, err := project.Store.Secrets().CreateSecretGrant(t.Context(), secretstore.CreateSecretGrantInput{
		OrgID: project.OrgUUID, SecretID: secret.ID,
		TargetProjectID: project.ProjectUUID, Actor: actor,
	})
	require.NoError(t, err)
	_, err = project.Store.Integrations().ConfigureIntegration(t.Context(), integrationstore.ConfigureIntegrationInput{
		OrgID: project.OrgUUID, ProjectID: project.ProjectUUID, IntegrationID: integration.ID,
		InstalledByUserID: project.AdminUserUUID, Provider: integration.Provider,
		ProviderTenantID: integration.ProviderTenantID, ProviderAccountRef: integration.ProviderAccountRef,
		ProviderConfig: integration.ProviderConfig, ProviderIdentity: integration.ProviderIdentity,
		CredentialSecretID: secret.ID, CredentialVersionID: version.ID,
		ExpectedSetupRevision: integration.SetupRevision,
	})
	require.NoError(t, err)
	return secretstore.DeleteSecretGrantInput{
		OrgID: project.OrgUUID, SecretID: secret.ID, GrantID: grant.ID, Actor: actor,
	}
}

func beginCallbackCredentialRevocation(
	t *testing.T, pool *pgxpool.Pool, grant secretstore.DeleteSecretGrantInput,
) pgx.Tx {
	t.Helper()
	tx := integrationdb.BeginTx(t, t.Context(), pool)
	// Hold the same exclusive secret lock as DeleteSecretGrant, then commit only
	// after the signed callback has reached its shared secret lock.
	_, err := tx.Exec(t.Context(), `SELECT id FROM secrets WHERE org_id=$1 AND id=$2 FOR UPDATE`,
		grant.OrgID, grant.SecretID)
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `DELETE FROM secret_grants WHERE org_id=$1 AND id=$2`, grant.OrgID, grant.GrantID)
	require.NoError(t, err)
	return tx
}

func TestCapturedDiscordCredentialRevocationPreservesDashboard(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"question", "permission"} {
		for _, timing := range []string{"committed", "concurrent"} {
			t.Run(kind+"/"+timing, func(t *testing.T) {
				t.Parallel()
				f := newCapturedHTTPFixture(t, "discord", kind)
				grant := grantCallbackCredential(t, f.project, f.integration)
				var response map[string]any
				if timing == "committed" {
					_, err := f.project.Store.Secrets().DeleteSecretGrant(t.Context(), grant)
					require.NoError(t, err)
					response = f.discordRequest(t, "c0", false, false)
				} else {
					tx := beginCallbackCredentialRevocation(t, f.pool, grant)
					done := integrationdb.RunAsync(func() (map[string]any, error) {
						return f.discordRequest(t, "c0", false, false), nil
					})
					integrationdb.WaitForNamedLockWaiters(t, t.Context(), f.pool, "LockSecretForReference", 1)
					lockCtx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					_, err := tx.Exec(lockCtx, `SELECT id FROM agents WHERE project_id=$1 AND id=$2 FOR UPDATE`,
						f.project.ProjectUUID, f.record.AgentID)
					require.NoError(t, err, "callback must wait for the credential before locking its agent")
					require.NoError(t, tx.Commit(t.Context()))
					response = integrationdb.AwaitSuccess(t, done, "callback after credential grant revocation")
				}
				require.Equal(t, float64(4), response["type"])
				current, found, err := f.project.Store.Execution().GetAgentInteraction(
					t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, executionstore.AgentInteractionStateOpen, current.State)
				require.Equal(t, uuid.Nil, current.ResolvedByInputID)
				require.JSONEq(t, string(f.record.Destination), string(current.Destination))
				require.JSONEq(t, string(f.record.PresentationReceipt), string(current.PresentationReceipt))
				path := f.project.ProjectPath + "/agents/" + testPublicID(t, publicid.KindAgent, f.record.AgentID) +
					"/interactions/" + testPublicID(t, publicid.KindAgentInteraction, f.record.ID) + "/resolve"
				resolved := requestJSONWithHeaders(t, f.handler, http.MethodPost, path,
					`{"answers":[{"option_indices":[0]}]}`, "", http.StatusOK, f.project.adminBrowserAuthHeaders())
				require.Equal(t, "resolved", resolved["state"])
			})
		}
	}
}

func TestDiscordProfileChoiceCredentialRevocation(t *testing.T) {
	t.Parallel()
	for _, timing := range []string{"committed", "concurrent"} {
		t.Run(timing, func(t *testing.T) {
			t.Parallel()
			f := newProfileChoiceHTTPFixture(t, "discord")
			menu := f.menu(t, false)
			grant := grantCallbackCredential(t, f.project, f.integration)
			var response *httptest.ResponseRecorder
			if timing == "committed" {
				_, err := f.project.Store.Secrets().DeleteSecretGrant(t.Context(), grant)
				require.NoError(t, err)
				response = f.callback(t, menu, menu.ID, "reviewer", "700", false, nil)
			} else {
				tx := beginCallbackCredentialRevocation(t, f.pool, grant)
				done := integrationdb.RunAsync(func() (*httptest.ResponseRecorder, error) {
					return f.callback(t, menu, menu.ID, "reviewer", "700", false, nil), nil
				})
				integrationdb.WaitForNamedLockWaiters(t, t.Context(), f.pool, "LockSecretForReference", 1)
				lockCtx, cancel := context.WithTimeout(t.Context(), time.Second)
				defer cancel()
				require.NoError(t, integrationstore.LockConversationTx(
					lockCtx, tx, f.project.ProjectUUID, f.integration.ID, menu.Address),
					"callback must wait for the credential before locking its conversation")
				require.NoError(t, tx.Commit(t.Context()))
				response = integrationdb.AwaitSuccess(t, done, "profile choice after credential grant revocation")
			}
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Contains(t, response.Body.String(), unavailableProfileChoice)
			require.Equal(t, menu, f.readChoice(t, menu.ID))
			f.assertReceiptCount(t, menu.ID, 0)
			f.assertNoAgent(t)
		})
	}
}

func TestDiscordCallbacksPreserveCredentialRotation(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"question", "permission", "profile_choice"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			if kind == "profile_choice" {
				f := newProfileChoiceHTTPFixture(t, "discord")
				menu := f.menu(t, false)
				grantCallbackCredential(t, f.project, f.integration)
				rotateCallbackCredential(t, f.project, f.integration)
				f.assertAccepted(t, f.callback(t, menu, menu.ID, "reviewer", "700", false, nil), menu)
				f.assertReceiptCount(t, menu.ID, 1)
				return
			}
			f := newCapturedHTTPFixture(t, "discord", kind)
			grantCallbackCredential(t, f.project, f.integration)
			rotateCallbackCredential(t, f.project, f.integration)
			require.Equal(t, float64(6), f.discordRequest(t, "c0", false, false)["type"])
			current, found, err := f.project.Store.Execution().GetAgentInteraction(
				t.Context(), f.project.ProjectUUID, f.record.AgentID, f.record.ID)
			require.NoError(t, err)
			require.True(t, found)
			require.Equal(t, executionstore.AgentInteractionStateResolved, current.State)
		})
	}
}

func rotateCallbackCredential(t *testing.T, project publicHTTPProject, integration integrationstore.IntegrationRecord) {
	t.Helper()
	integration, err := project.Store.Integrations().GetIntegration(t.Context(), project.ProjectUUID, integration.ID)
	require.NoError(t, err)
	_, _, err = project.Store.Secrets().CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
		OrgID: project.OrgUUID, SecretID: integration.CredentialSecretID,
		Actor:    httpUserPrincipal(project.AdminUserUUID),
		Material: secrets.GenericMaterial{Value: "rotated-bot-token"},
	})
	require.NoError(t, err)
	current, err := project.Store.Integrations().GetIntegration(t.Context(), project.ProjectUUID, integration.ID)
	require.NoError(t, err)
	require.Equal(t, integration.SetupRevision, current.SetupRevision)
}
