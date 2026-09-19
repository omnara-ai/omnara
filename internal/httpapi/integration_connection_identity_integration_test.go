//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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

type connectionIdentityFixture struct {
	handler    http.Handler
	project    publicHTTPProject
	connection integrationstore.IntegrationConnectionRecord
	material   secrets.Material
	body       map[string]any
	steps      int
}

func newConnectionIdentityFixture(
	t *testing.T, provider string, before func(context.Context) error,
) connectionIdentityFixture {
	t.Helper()
	var f connectionIdentityFixture
	if provider == "github" {
		config := githubSetupTestConfig(t)
		config.BeforeRequest = before
		journey := newGitHubHTTPJourney(t, "github-identity", WithGitHubClientConfig(config))
		f.handler, f.project, f.connection = journey.handler, journey.project, journey.connection
		credential, err := f.project.Store.Secrets().ReadProjectAvailableSecretPayload(t.Context(),
			secretstore.ReadProjectAvailableSecretPayloadInput{
				OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
				SecretID: f.connection.CredentialSecretID, Kind: secrets.KindGitHubAppCredentials,
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
		f.handler = newIntegrationServer(openIntegrationDB(t, t.Context()), WithDiscordClientConfig(config))
		f.project = bootstrapPublicHTTPProject(t, f.handler, "discord-identity")
		secretID := createConnectionHTTPSecret(t, f.handler, f.project, "discord-credentials",
			map[string]any{"kind": "generic", "value": "private-discord-token"})
		body := connectionHTTPBody("discord", "111", "222")
		body["credential_secret_id"] = secretID
		f.connection = discordHTTPConnection(t, f.handler, f.project, body)
		f.material, f.steps = secrets.GenericMaterial{Value: "private-discord-token"}, 2
	}
	f.body = connectionHTTPBody(provider, f.connection.ProviderTenantID, f.connection.ProviderAccountRef)
	f.body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, f.connection.CredentialSecretID)
	return f
}

func (f connectionIdentityFixture) update(t *testing.T, status int) {
	t.Helper()
	path := f.project.ProjectPath + "/integration-connections/" +
		testPublicID(t, publicid.KindIntegrationConnection, f.connection.ID)
	requestJSONWithHeaders(t, f.handler, http.MethodPut, path, projectAppHTTPJSON(t, f.body),
		"", status, authHeaders(f.project.AdminToken))
}

func (f connectionIdentityFixture) current(t *testing.T) integrationstore.IntegrationConnectionRecord {
	t.Helper()
	current, err := f.project.Store.Integrations().GetIntegrationConnection(
		t.Context(), f.project.ProjectUUID, f.connection.ID)
	require.NoError(t, err)
	return current
}

func (f connectionIdentityFixture) rotate(t *testing.T) uuid.UUID {
	t.Helper()
	_, version, err := f.project.Store.Secrets().CreateSecretVersion(t.Context(), secretstore.CreateSecretVersionInput{
		OrgID: f.project.OrgUUID, SecretID: f.connection.CredentialSecretID, Material: f.material,
		Actor: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
	})
	require.NoError(t, err)
	return version.ID
}

func verifiedConnectionVersion(t *testing.T, record integrationstore.IntegrationConnectionRecord) uuid.UUID {
	t.Helper()
	var metadata struct {
		VersionID uuid.UUID `json:"verified_credential_version_id"`
	}
	require.NoError(t, json.Unmarshal(record.ProviderMetadata, &metadata))
	require.NotEqual(t, uuid.Nil, metadata.VersionID)
	return metadata.VersionID
}

func TestConnectionHTTPIdentityRepairAndCredentialRotation(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "discord"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			calls, offline := 0, false
			f := newConnectionIdentityFixture(t, provider, func(context.Context) error {
				calls++
				if offline {
					return errors.New("provider unavailable")
				}
				return nil
			})
			require.Equal(t, f.steps, calls)
			verifiedConnectionVersion(t, f.connection)
			// Disabling an existing credential binding remains independent of
			// provider availability. Discord can also save active settings offline.
			offline = true
			if provider == "discord" {
				f.body["provider_agent_display_name"] = "Updated active label"
				f.update(t, http.StatusOK)
				require.Equal(t, f.steps, calls)
			}
			f.body["provider_agent_display_name"], f.body["state"] = "Updated label", "disabled"
			f.update(t, http.StatusOK)
			current := f.current(t)
			require.Equal(t, f.steps, calls)
			require.JSONEq(t, string(f.connection.ProviderIdentity), string(current.ProviderIdentity))
			require.JSONEq(t, string(f.connection.ProviderMetadata), string(current.ProviderMetadata))
			// An existing record with missing bot facts is repaired by public PUT,
			// even when its credential revision was previously marked verified.
			offline = false
			_, err := integrationPoolForHandler(t, f.handler).Exec(t.Context(),
				`UPDATE integration_connections SET provider_identity='{}' WHERE id=$1`, f.connection.ID)
			require.NoError(t, err)
			f.body["state"] = "active"
			f.update(t, http.StatusOK)
			require.Equal(t, 2*f.steps, calls)
			repaired := f.current(t)
			require.JSONEq(t, string(f.connection.ProviderIdentity), string(repaired.ProviderIdentity))
			version := f.rotate(t)
			f.update(t, http.StatusOK)
			require.Equal(t, 3*f.steps, calls, "same-secret rotation must rediscover identity")
			require.Equal(t, version, verifiedConnectionVersion(t, f.current(t)))
			// A new credential reference must also be independently verified.
			secret, _, err := f.project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
				OrgID: f.project.OrgUUID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project.ProjectUUID,
				Name: "replacement", Material: f.material, Actor: identitystore.NewUserPrincipal(f.project.AdminUserUUID),
			})
			require.NoError(t, err)
			f.body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, secret.ID)
			f.update(t, http.StatusOK)
			require.Equal(t, 4*f.steps, calls)
			require.Equal(t, secret.ID, f.current(t).CredentialSecretID)
		})
	}
}

func TestConnectionHTTPIdentitySaveFencesConcurrentChanges(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "discord"} {
		for _, change := range []string{"credential rotation", "connection update", "connection deletion"} {
			t.Run(provider+"/"+change, func(t *testing.T) {
				t.Parallel()
				var before func()
				f := newConnectionIdentityFixture(t, provider, func(context.Context) error {
					if before != nil {
						action := before
						before = nil
						action()
					}
					return nil
				})
				version := f.rotate(t) // Require fresh verification on the next PUT.
				before = func() {
					switch change {
					case "credential rotation":
						f.rotate(t)
					case "connection update":
						input := integrationstore.SaveIntegrationConnectionInput{
							OrgID: f.project.OrgUUID, ProjectID: f.project.ProjectUUID,
							InstalledByUserID: f.project.AdminUserUUID, Provider: provider,
							ProviderTenantID: f.connection.ProviderTenantID, ProviderAccountRef: f.connection.ProviderAccountRef,
							State: f.connection.State, CredentialSecretID: f.connection.CredentialSecretID,
							CredentialVersionID: version, CredentialAppID: 123, ProviderAgentDisplayName: "Concurrent edit",
						}
						_, err := f.project.Store.Integrations().UpdateIntegrationConnection(t.Context(), f.connection.ID, input)
						require.NoError(t, err)
					case "connection deletion":
						require.NoError(t, f.project.Store.Integrations().DeleteIntegrationConnection(
							t.Context(), f.project.ProjectUUID, f.connection.ID))
					}
				}
				status := http.StatusConflict
				if change == "connection deletion" {
					status = http.StatusNotFound
				}
				f.update(t, status)
				require.Nil(t, before)
				if change == "connection deletion" {
					_, err := f.project.Store.Integrations().GetIntegrationConnection(
						t.Context(), f.project.ProjectUUID, f.connection.ID)
					require.ErrorIs(t, err, storeerr.ErrNotFound, "verified save must not resurrect a deleted connection")
					return
				}
				current := f.current(t)
				require.JSONEq(t, string(f.connection.ProviderMetadata), string(current.ProviderMetadata))
				require.JSONEq(t, string(f.connection.ProviderIdentity), string(current.ProviderIdentity))
				if change == "connection update" {
					require.Equal(t, "Concurrent edit", current.ProviderAgentDisplayName)
				} else {
					require.Equal(t, f.connection.UpdatedAt, current.UpdatedAt)
				}
			})
		}
	}
}

func TestConnectionHTTPIdentitySaveRechecksCredentialGrant(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{"github", "discord"} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			var revoke func()
			f := newConnectionIdentityFixture(t, provider, func(context.Context) error {
				if revoke != nil {
					action := revoke
					revoke = nil
					action()
				}
				return nil
			})
			other := projectAppHTTPSecondProject(t, f.handler, f.project)
			actor := identitystore.NewUserPrincipal(f.project.AdminUserUUID)
			secret, _, err := f.project.Store.Secrets().CreateSecret(t.Context(), secretstore.CreateSecretInput{
				OrgID: f.project.OrgUUID, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: other.ProjectUUID,
				Name: "granted-replacement", Material: f.material, Actor: actor,
			})
			require.NoError(t, err)
			grant, err := f.project.Store.Secrets().CreateSecretGrant(t.Context(), secretstore.CreateSecretGrantInput{
				OrgID: f.project.OrgUUID, SecretID: secret.ID, TargetProjectID: f.project.ProjectUUID, Actor: actor,
			})
			require.NoError(t, err)
			f.body["credential_secret_id"] = testPublicID(t, publicid.KindSecret, secret.ID)
			revoke = func() {
				_, err := f.project.Store.Secrets().DeleteSecretGrant(t.Context(), secretstore.DeleteSecretGrantInput{
					OrgID: f.project.OrgUUID, SecretID: secret.ID, GrantID: grant.ID, Actor: actor,
				})
				require.NoError(t, err)
			}
			f.update(t, http.StatusNotFound)
			require.Nil(t, revoke)
			current := f.current(t)
			require.Equal(t, f.connection.CredentialSecretID, current.CredentialSecretID)
			require.Equal(t, f.connection.UpdatedAt, current.UpdatedAt)
			require.JSONEq(t, string(f.connection.ProviderMetadata), string(current.ProviderMetadata))
		})
	}
}

func TestGitHubHTTPRepairCannotChangeKnownBotIdentity(t *testing.T) {
	t.Parallel()
	f := newConnectionIdentityFixture(t, "github", nil)
	_, err := integrationPoolForHandler(t, f.handler).Exec(t.Context(),
		`UPDATE integration_connections SET provider_identity='{"bot_user_id":888}' WHERE id=$1`, f.connection.ID)
	require.NoError(t, err)
	f.update(t, http.StatusBadRequest)
	current := f.current(t)
	require.JSONEq(t, `{"bot_user_id":888}`, string(current.ProviderIdentity))
	require.Equal(t, f.connection.UpdatedAt, current.UpdatedAt)
}

func TestGitHubHTTPActiveSaveRequiresProviderButDisableAndDeleteDoNot(t *testing.T) {
	t.Parallel()
	calls, offline := 0, false
	f := newConnectionIdentityFixture(t, "github", func(context.Context) error {
		calls++
		if offline {
			return &github.APIError{Code: github.TransientFailure, StatusCode: http.StatusServiceUnavailable}
		}
		return nil
	})
	offline = true
	f.body["provider_agent_display_name"] = "Edited settings"
	f.update(t, http.StatusServiceUnavailable)
	require.Equal(t, f.steps+1, calls)
	require.Equal(t, f.connection.UpdatedAt, f.current(t).UpdatedAt)
	f.body["state"] = "disabled"
	f.update(t, http.StatusOK)
	require.Equal(t, f.steps+1, calls, "disable must not call the provider")
	require.Equal(t, integrationstore.IntegrationConnectionStateDisabled, f.current(t).State)
	f.body["state"] = "active"
	f.update(t, http.StatusServiceUnavailable)
	require.Equal(t, f.steps+2, calls, "re-enable must refresh identity")
	require.Equal(t, integrationstore.IntegrationConnectionStateDisabled, f.current(t).State)
	path := f.project.ProjectPath + "/integration-connections/" +
		testPublicID(t, publicid.KindIntegrationConnection, f.connection.ID)
	requestJSONWithHeaders(t, f.handler, http.MethodDelete, path, "", "", http.StatusNoContent,
		authHeaders(f.project.AdminToken))
	require.Equal(t, f.steps+2, calls, "delete must not call the provider")
}
