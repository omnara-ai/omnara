//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/github"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type appSetupIdentityFixture struct {
	handler  http.Handler
	project  publicHTTPProject
	app      integrationstore.ProjectAppRecord
	material secrets.Material
	body     map[string]any
	steps    int
}

func newAppSetupIdentityFixture(
	t *testing.T, provider string, before func(context.Context) error,
) appSetupIdentityFixture {
	t.Helper()
	var f appSetupIdentityFixture
	if provider == "github" {
		config := githubSetupTestConfig(t)
		config.BeforeRequest = before
		journey := newGitHubSetupJourney(t, "github-identity", WithGitHubClientConfig(config))
		f.handler, f.project, f.app = journey.handler, journey.project, journey.app
		credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(t.Context(),
			secretstore.ReadProjectAvailableSecretPayloadInput{
				OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
				SecretID: f.app.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
			})
		require.NoError(t, err)
		f.material = secrets.GitHubAppCredentialsMaterial{
			AppID: "123", PrivateKey: credential.Payload[secrets.KeyPrivateKey],
			WebhookSecret: githubJourneyWebhookSecret,
		}
		f.steps = 5
	} else {
		config := discordSetupTestConfig(t)
		config.BeforeRequest = before
		f.handler = newIntegrationServer(
			openIntegrationDB(t, t.Context()),
			WithDiscordClientConfig(config),
		)
		f.project = bootstrapPublicHTTPProject(t, f.handler, "discord-identity")
		secretID := createAppSetupHTTPSecret(t, f.handler, f.project, "discord-credentials",
			map[string]any{"kind": "generic", "value": "private-discord-token"})
		body := appSetupHTTPBody("111", "222")
		body["credential_secret_id"] = secretID
		f.app = configureDiscordHTTPApp(t, f.handler, f.project, body)
		f.material, f.steps = secrets.GenericMaterial{Value: "private-discord-token"}, 2
	}
	f.body = appSetupHTTPBody(f.app.ProviderTenantID,
		f.app.ProviderAccountRef,
	)
	f.body["credential_secret_id"] = testPublicID(
		t,
		publicid.KindSecret,
		f.app.CredentialSecretID,
	)
	f.body["expected_setup_revision"] = f.app.SetupRevision
	return f
}

func (f appSetupIdentityFixture) update(t *testing.T, status int) {
	t.Helper()
	path := appSetupPath(t, f.project, f.app)
	response := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		path,
		projectAppHTTPJSON(t, f.body),
		"",
		status,
		authHeaders(f.project.AdminToken),
	)
	if status == http.StatusOK {
		f.body["expected_setup_revision"] = response["setup_revision"]
	}

}

func (f appSetupIdentityFixture) current(t *testing.T) integrationstore.ProjectAppRecord {
	t.Helper()
	current, err := f.project.Store.Integrations().GetProjectApp(
		t.Context(), f.project.ProjectUUID, f.app.ID)
	require.NoError(t, err)
	return current
}

func (f appSetupIdentityFixture) rotate(t *testing.T) uuid.UUID {
	t.Helper()
	_, version, err := f.project.Store.Secrets().
		CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
			OrgID:    f.project.OrgUUID,
			SecretID: f.app.CredentialSecretID,
			Material: f.material,
			Actor:    identitystore.NewUserPrincipal(f.project.AdminUserUUID),
		})
	require.NoError(t, err)
	return version.ID
}

func verifiedAppCredentialVersion(
	t *testing.T,
	record integrationstore.ProjectAppRecord,
) uuid.UUID {
	t.Helper()
	var metadata struct {
		VersionID uuid.UUID `json:"verified_credential_version_id"`
	}
	require.NoError(t, json.Unmarshal(record.ProviderMetadata, &metadata))
	require.NotEqual(t, uuid.Nil, metadata.VersionID)
	return metadata.VersionID
}

func TestAppSetupHTTPIdentityRepairAndCredentialRotation(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "discord"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			calls, offline := 0, false
			f := newAppSetupIdentityFixture(t, provider, func(context.Context) error {
				calls++
				if offline {
					return errors.New("provider unavailable")
				}
				return nil
			})
			require.Equal(t, f.steps, calls)
			verifiedAppCredentialVersion(t, f.app)
			// Disconnecting an existing credential binding remains independent of
			// provider availability. Discord can also save active settings offline.
			offline = true
			if provider == "discord" {
				f.body["provider_agent_display_name"] = "Updated active label"
				f.update(t, http.StatusOK)
				require.Equal(t, "Updated active label", f.current(t).ProviderAgentDisplayName)
				require.Equal(t, f.steps, calls)
			}
			f.disconnect(t)
			current := f.current(t)
			require.Equal(t, f.steps, calls)
			require.JSONEq(
				t,
				string(f.app.ProviderIdentity),
				string(current.ProviderIdentity),
			)
			require.JSONEq(
				t,
				string(f.app.ProviderMetadata),
				string(current.ProviderMetadata),
			)
			// An existing record with missing bot facts is repaired by public setup,
			// even when its credential revision was previously marked verified.
			offline = false
			_, err := integrationPoolForHandler(t, f.handler).Exec(t.Context(),
				`UPDATE project_apps SET provider_identity='{}' WHERE id=$1`, f.app.ID)
			require.NoError(t, err)
			f.update(t, http.StatusOK)
			require.Equal(t, 2*f.steps, calls)
			repaired := f.current(t)
			require.JSONEq(
				t,
				string(f.app.ProviderIdentity),
				string(repaired.ProviderIdentity),
			)
			version := f.rotate(t)
			f.update(t, http.StatusOK)
			require.Equal(t, 3*f.steps, calls, "same-secret rotation must rediscover identity")
			require.Equal(t, version, verifiedAppCredentialVersion(t, f.current(t)))
			// A new credential reference must also be independently verified.
			secret, _, err := f.project.Store.Secrets().
				CreateSecret(t.Context(), secretstore.CreateSecretInput{
					OrgID:          f.project.OrgUUID,
					OwnerKind:      secretstore.SecretOwnerProject,
					OwnerProjectID: f.project.ProjectUUID,
					Name:           "replacement",
					Material:       f.material,
					Actor:          identitystore.NewUserPrincipal(f.project.AdminUserUUID),
				})
			require.NoError(t, err)
			f.body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, secret.ID)
			f.update(t, http.StatusOK)
			require.Equal(t, 4*f.steps, calls)
			require.Equal(t, secret.ID, f.current(t).CredentialSecretID)
		})
	}
}

func TestAppSetupHTTPIdentitySaveFencesConcurrentChanges(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "discord"} {
		for _, change := range []string{"credential rotation", "app setup", "app deletion"} {
			t.Run(provider+"/"+change, func(t *testing.T) {
				t.Parallel()
				var before func()
				f := newAppSetupIdentityFixture(t, provider, func(context.Context) error {
					if before != nil {
						action := before
						before = nil
						action()
					}
					return nil
				})
				version := f.rotate(t) // Require fresh verification on the next setup.
				before = func() {
					switch change {
					case "credential rotation":
						f.rotate(t)
					case "app setup":
						input := integrationstore.ConfigureProjectAppInput{
							OrgID:                    f.project.OrgUUID,
							ProjectID:                f.project.ProjectUUID,
							InstalledByUserID:        f.project.AdminUserUUID,
							Provider:                 provider,
							ProviderTenantID:         f.app.ProviderTenantID,
							ProviderAccountRef:       f.app.ProviderAccountRef,
							AppID:                    f.app.ID,
							ExpectedSetupRevision:    f.app.SetupRevision,
							CredentialSecretID:       f.app.CredentialSecretID,
							ProviderIdentity:         f.app.ProviderIdentity,
							ProviderMetadata:         f.app.ProviderMetadata,
							CredentialVersionID:      version,
							CredentialAppID:          123,
							ProviderAgentDisplayName: "Concurrent edit",
						}
						_, err := f.project.Store.Integrations().
							ConfigureProjectApp(t.Context(), input)
						require.NoError(t, err)
					case "app deletion":
						require.NoError(t, f.project.Store.Integrations().DeleteProjectApp(
							t.Context(), f.project.OrgUUID, f.project.ProjectUUID, f.app.ID))
					}
				}
				status := http.StatusConflict
				if change == "app deletion" {
					status = http.StatusNotFound
				}
				f.update(t, status)
				require.Nil(t, before)
				if change == "app deletion" {
					_, err := f.project.Store.Integrations().GetProjectApp(
						t.Context(), f.project.ProjectUUID, f.app.ID)
					require.ErrorIs(
						t,
						err,
						storeerr.ErrNotFound,
						"verified save must not resurrect a deleted app",
					)
					return
				}
				current := f.current(t)
				require.JSONEq(
					t,
					string(f.app.ProviderMetadata),
					string(current.ProviderMetadata),
				)
				require.JSONEq(
					t,
					string(f.app.ProviderIdentity),
					string(current.ProviderIdentity),
				)
				if change == "app setup" {
					require.Equal(t, "Concurrent edit", current.ProviderAgentDisplayName)
				} else {
					require.Equal(t, f.app.SetupRevision, current.SetupRevision)
				}
			})
		}
	}
}

func TestAppSetupHTTPIdentitySaveRechecksCredentialGrant(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "discord"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			var revoke func()
			f := newAppSetupIdentityFixture(t, provider, func(context.Context) error {
				if revoke != nil {
					action := revoke
					revoke = nil
					action()
				}
				return nil
			})
			other := projectAppHTTPSecondProject(t, f.handler, f.project)
			actor := identitystore.NewUserPrincipal(f.project.AdminUserUUID)
			secret, _, err := f.project.Store.Secrets().
				CreateSecret(t.Context(), secretstore.CreateSecretInput{
					OrgID:          f.project.OrgUUID,
					OwnerKind:      secretstore.SecretOwnerProject,
					OwnerProjectID: other.ProjectUUID,
					Name:           "granted-replacement",
					Material:       f.material,
					Actor:          actor,
				})
			require.NoError(t, err)
			grant, err := f.project.Store.Secrets().
				CreateSecretGrant(t.Context(), secretstore.CreateSecretGrantInput{
					OrgID:           f.project.OrgUUID,
					SecretID:        secret.ID,
					TargetProjectID: f.project.ProjectUUID,
					Actor:           actor,
				})
			require.NoError(t, err)
			f.body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, secret.ID)
			revoke = func() {
				_, err := f.project.Store.Secrets().
					DeleteSecretGrant(t.Context(), secretstore.DeleteSecretGrantInput{
						OrgID:    f.project.OrgUUID,
						SecretID: secret.ID,
						GrantID:  grant.ID,
						Actor:    actor,
					})
				require.NoError(t, err)
			}
			f.update(t, http.StatusNotFound)
			require.Nil(t, revoke)
			current := f.current(t)
			require.Equal(t, f.app.CredentialSecretID, current.CredentialSecretID)
			require.Equal(t, f.app.SetupRevision, current.SetupRevision)
			require.JSONEq(
				t,
				string(f.app.ProviderMetadata),
				string(current.ProviderMetadata),
			)
		})
	}
}

func TestGitHubHTTPRepairCannotChangeKnownBotIdentity(t *testing.T) {
	t.Parallel()
	f := newAppSetupIdentityFixture(t, "github", nil)
	_, err := integrationPoolForHandler(t, f.handler).Exec(t.Context(),
		`UPDATE project_apps SET provider_identity='{"bot_user_id":888}' WHERE id=$1`, f.app.ID)
	require.NoError(t, err)
	f.update(t, http.StatusBadRequest)
	current := f.current(t)
	require.JSONEq(t, `{"bot_user_id":888}`, string(current.ProviderIdentity))
	require.Equal(t, f.app.SetupRevision, current.SetupRevision)
}

func TestGitHubHTTPActiveSaveRequiresProviderButDisconnectAndDeleteDoNot(t *testing.T) {
	t.Parallel()
	calls, offline := 0, false
	f := newAppSetupIdentityFixture(t, "github", func(context.Context) error {
		calls++
		if offline {
			return &github.APIError{
				Code:       github.TransientFailure,
				StatusCode: http.StatusServiceUnavailable,
			}
		}
		return nil
	})
	offline = true
	f.body["provider_agent_display_name"] = "Edited settings"
	f.update(t, http.StatusServiceUnavailable)
	require.Equal(t, f.steps+1, calls)
	require.Equal(t, f.app.SetupRevision, f.current(t).SetupRevision)
	f.disconnect(t)
	require.Equal(t, f.steps+1, calls, "disconnect must not call the provider")
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, f.current(t).State)
	f.update(t, http.StatusServiceUnavailable)
	require.Equal(t, f.steps+2, calls, "reconnect must refresh identity")
	require.Equal(t, integrationstore.ProjectAppStateDisconnected, f.current(t).State)
	path := strings.TrimSuffix(appSetupPath(t, f.project, f.app), "/setup")
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path, "", "", http.StatusNoContent,
		authHeaders(f.project.AdminToken))
	require.Equal(t, f.steps+2, calls, "delete must not call the provider")
}

func (f appSetupIdentityFixture) disconnect(t *testing.T) {
	t.Helper()
	response := requestJSONWithHeaders(
		t,
		f.handler,
		http.MethodPost,
		strings.TrimSuffix(appSetupPath(t, f.project, f.app), "/setup")+"/disconnect",
		"",
		"",
		http.StatusOK,
		authHeaders(f.project.AdminToken),
	)
	f.body["expected_setup_revision"] = response["setup_revision"]
}
