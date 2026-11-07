//go:build integration

package storage

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestProviderHeaderReferencesLifecycle(t *testing.T) {
	t.Parallel()
	f := newSecretConsumerFixture(t, "secret")
	ctx := t.Context()
	credential, _, err := f.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
		OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "provider-credential",
		Actor: userPrincipal(f.admin.ID), Material: secrets.GenericMaterial{Value: "provider-key"},
	})
	require.NoError(t, err)
	headers := json.RawMessage(`{"X-Gateway-Key":"` + f.secret.ID.String() + `"}`)
	config, err := f.store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID: testOrgID, Name: "gateway", APIFormat: modelprotocol.APIFormatOpenAIResponses,
		BaseURL: "https://provider.example.test", CredentialSecretID: credential.ID, SecretHeaders: json.RawMessage(`{"X-Gateway-Key":"` + strings.ToUpper(f.secret.ID.String()) + `"}`),
	})
	require.NoError(t, err)
	require.JSONEq(t, string(headers), string(config.SecretHeaders), "create normalizes UUIDs exactly like GET")
	require.ErrorIs(t, f.deleteOwner(), storeerr.ErrConflict)
	read, err := f.store.Models().GetModelProviderConfig(ctx, testOrgID, config.ID)
	require.NoError(t, err)
	require.JSONEq(t, string(headers), string(read.SecretHeaders))
	listed, err := f.store.Models().ListModelProviderConfigs(ctx, modelstore.ListModelProviderConfigsInput{
		OrgID: testOrgID, Limit: 10,
	})
	require.NoError(t, err)
	var found bool
	for _, item := range listed.Configs {
		if item.ID == config.ID {
			require.JSONEq(t, string(headers), string(item.SecretHeaders))
			found = true
		}
	}
	require.True(t, found)
	patched, err := f.store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
		OrgID: testOrgID, ID: config.ID,
	})
	require.NoError(t, err, "an unrelated patch preserves normalized references")
	require.JSONEq(t, string(headers), string(patched.SecretHeaders))
	require.ErrorIs(t, f.deleteOwner(), storeerr.ErrConflict)
	empty := json.RawMessage(`{}`)
	patched, err = f.store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
		OrgID: testOrgID, ID: config.ID, SecretHeaders: &empty,
	})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(patched.SecretHeaders))
	require.NoError(t, f.deleteOwner(), "removing the header releases its secret")
	// Reuse the credential as a header to prove provider deletion removes both
	// kinds of reference rather than merely clearing its credential column.
	headers = json.RawMessage(`{"X-Gateway-Key":"` + credential.ID.String() + `"}`)
	_, err = f.store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
		OrgID: testOrgID, ID: config.ID, SecretHeaders: &headers,
	})
	require.NoError(t, err)
	err = f.store.Models().DeleteModelProviderConfig(ctx, testOrgID, config.ID)
	require.NoError(t, err)
	require.ErrorIs(t, f.store.Models().DeleteModelProviderConfig(ctx, testOrgID, config.ID), storeerr.ErrNotFound)
	_, err = f.store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
		OrgID: testOrgID, ID: config.ID,
	})
	require.ErrorIs(t, err, storeerr.ErrNotFound)
	_, err = f.store.Secrets().DeleteSecret(ctx, secretstore.DeleteSecretInput{
		OrgID: testOrgID, SecretID: credential.ID, Actor: userPrincipal(f.admin.ID),
	})
	require.NoError(t, err)
	var remaining int
	require.NoError(t, f.store.pool.QueryRow(ctx,
		"SELECT count(*) FROM model_provider_header_secrets WHERE provider_config_id = $1", config.ID).Scan(&remaining))
	require.Zero(t, remaining)
}

func TestProviderHeaderAdmissionSerializesWithSecretDeletion(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"provider", "deletion"} {
		t.Run(first, func(t *testing.T) {
			t.Parallel()
			f := newSecretConsumerFixture(t, "secret")
			ctx := t.Context()
			credential, _, err := f.store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
				OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: "provider-credential",
				Actor: userPrincipal(f.admin.ID), Material: secrets.GenericMaterial{Value: "provider-key"},
			})
			require.NoError(t, err)
			create := func() (modelstore.ModelProviderConfigRecord, error) {
				return f.store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
					OrgID: testOrgID, Name: "gateway", APIFormat: modelprotocol.APIFormatOpenAIResponses,
					BaseURL: "https://provider.example.test", CredentialSecretID: credential.ID,
					SecretHeaders: json.RawMessage(`{"X-Gateway-Key":"` + f.secret.ID.String() + `"}`),
				})
			}
			blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
			if first == "provider" {
				_, err := blocker.Exec(ctx, "LOCK TABLE model_provider_configs IN SHARE MODE")
				require.NoError(t, err)
				created := integrationdb.RunAsync(create)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "InsertModelProviderConfig", 1)
				deleted := integrationdb.RunAsyncError(f.deleteOwner)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockSecret", 1)
				require.NoError(t, blocker.Commit(ctx))
				integrationdb.AwaitSuccess(t, created, "provider admission")
				require.ErrorIs(t, integrationdb.Await(t, deleted, "secret deletion"), storeerr.ErrConflict)
				return
			}
			_, err = blocker.Exec(ctx, "SELECT id FROM secret_versions WHERE secret_id = $1 FOR UPDATE", f.secret.ID)
			require.NoError(t, err)
			deleted := integrationdb.RunAsyncError(f.deleteOwner)
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "DeleteSecretVersions", 1)
			created := integrationdb.RunAsync(create)
			integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockSecretForReference", 1)
			require.NoError(t, blocker.Commit(ctx))
			require.NoError(t, integrationdb.Await(t, deleted, "secret deletion"))
			admissionErr := integrationdb.Await(t, created, "provider admission").Err
			require.ErrorIs(t, admissionErr, storeerr.ErrInvalidRequest)
			require.ErrorContains(t, admissionErr, "secret_headers.X-Gateway-Key")
		})
	}
}

func TestProviderHeaderPatchReadsReferencesAfterLockWait(t *testing.T) {
	t.Parallel()
	f := newSecretConsumerFixture(t, "secret")
	ctx := t.Context()
	config, err := f.store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
		OrgID: testOrgID, Name: "gateway", APIFormat: modelprotocol.APIFormatOpenAIResponses,
		BaseURL: "https://provider.example.test", CredentialSecretID: f.secret.ID,
	})
	require.NoError(t, err)
	blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
	_, err = blocker.Exec(ctx, "SELECT id FROM model_provider_configs WHERE id = $1 FOR UPDATE", config.ID)
	require.NoError(t, err)
	headers := json.RawMessage(`{"X-Gateway-Key":"` + strings.ToUpper(f.secret.ID.String()) + `"}`)
	first := integrationdb.RunAsync(func() (modelstore.ModelProviderConfigRecord, error) {
		return f.store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
			OrgID: testOrgID, ID: config.ID, SecretHeaders: &headers,
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockModelProviderConfigForMutation", 1)
	timeout := config.RequestTimeoutMS + 1000
	second := integrationdb.RunAsync(func() (modelstore.ModelProviderConfigRecord, error) {
		return f.store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
			OrgID: testOrgID, ID: config.ID, RequestTimeoutMS: &timeout,
		})
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockModelProviderConfigForMutation", 2)
	require.NoError(t, blocker.Commit(ctx))
	firstResult := integrationdb.AwaitSuccess(t, first, "header patch")
	secondResult := integrationdb.AwaitSuccess(t, second, "unrelated patch")
	read, err := f.store.Models().GetModelProviderConfig(ctx, testOrgID, config.ID)
	require.NoError(t, err)
	expected := `{"X-Gateway-Key":"` + f.secret.ID.String() + `"}`
	require.JSONEq(t, expected, string(firstResult.SecretHeaders))
	require.JSONEq(t, expected, string(secondResult.SecretHeaders))
	require.JSONEq(t, expected, string(read.SecretHeaders))
	require.Equal(t, timeout, read.RequestTimeoutMS)
	_, err = f.store.pool.Exec(ctx, `UPDATE model_provider_header_secrets
SET management_kind = 'cluster' WHERE provider_config_id = $1`, config.ID)
	require.Error(t, err, "typed references cannot cross the parent or secret management boundary")
}

func TestProviderDeletionSerializesWithOrganizationDeletion(t *testing.T) {
	t.Parallel()
	for _, first := range []string{"provider", "organization"} {
		t.Run(first, func(t *testing.T) {
			t.Parallel()
			f := newSecretConsumerFixture(t, "secret")
			ctx := t.Context()
			config, err := f.store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
				OrgID: testOrgID, Name: "gateway", APIFormat: modelprotocol.APIFormatOpenAIResponses,
				BaseURL: "https://provider.example.test", CredentialSecretID: f.secret.ID,
				SecretHeaders: json.RawMessage(`{"X-Gateway-Key":"` + f.secret.ID.String() + `"}`),
			})
			require.NoError(t, err)
			deleteProvider := func() error {
				return f.store.Models().DeleteModelProviderConfig(ctx, testOrgID, config.ID)
			}
			deleteOrganization := func() error {
				_, deleteErr := f.store.Organizations().DeleteOrganizationOnceForIntegration(
					ctx, testOrgID, userPrincipal(f.admin.ID),
				)
				return deleteErr
			}
			// Pause the first deletion at the child row. It must retain its
			// lifecycle gate until the whole provider/header mutation commits.
			blocker := integrationdb.BeginTx(t, ctx, f.store.pool)
			_, err = blocker.Exec(ctx, `SELECT provider_config_id FROM model_provider_header_secrets
WHERE provider_config_id = $1 FOR UPDATE`, config.ID)
			require.NoError(t, err)
			var providerDone, organizationDone <-chan error
			if first == "provider" {
				providerDone = integrationdb.RunAsyncError(deleteProvider)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "DeleteModelProviderHeaderSecrets", 1)
				organizationDone = integrationdb.RunAsyncError(deleteOrganization)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockOrganizationLifecycleExclusive", 1)
			} else {
				organizationDone = integrationdb.RunAsyncError(deleteOrganization)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "DeleteOrganizationModelProviderHeaderSecrets", 1)
				providerDone = integrationdb.RunAsyncError(deleteProvider)
				integrationdb.WaitForNamedLockWaiters(t, ctx, f.store.pool, "LockOrganizationLifecycleShared", 1)
			}
			require.NoError(t, blocker.Commit(ctx))
			providerErr := integrationdb.Await(t, providerDone, "provider deletion")
			if first == "provider" {
				require.NoError(t, providerErr)
			} else {
				require.ErrorIs(t, providerErr, storeerr.ErrNotFound)
			}
			require.NoError(t, integrationdb.Await(t, organizationDone, "organization deletion"))
			var retired bool
			require.NoError(t, f.store.pool.QueryRow(ctx, `SELECT deleted_at IS NOT NULL AND credential_secret_id IS NULL
FROM model_provider_configs WHERE id = $1`, config.ID).Scan(&retired))
			require.True(t, retired)
			var remaining int
			require.NoError(t, f.store.pool.QueryRow(ctx,
				"SELECT count(*) FROM model_provider_header_secrets WHERE org_id = $1", testOrgID).Scan(&remaining))
			require.Zero(t, remaining)
			require.NoError(t, f.store.pool.QueryRow(ctx,
				"SELECT count(*) FROM secret_versions WHERE secret_id = $1", f.secret.ID).Scan(&remaining))
			require.Zero(t, remaining)
		})
	}
}

func TestSecretReferenceGuardCoversForeignKeys(t *testing.T) {
	t.Parallel()
	pool := openIntegrationDB(t, t.Context())
	rows, err := pool.Query(t.Context(), `
SELECT relation.relname || '.' || attribute.attname
FROM pg_constraint constraint_row
JOIN pg_class relation ON relation.oid = constraint_row.conrelid
CROSS JOIN LATERAL unnest(constraint_row.conkey, constraint_row.confkey) keys(local_key, foreign_key)
JOIN pg_attribute attribute ON attribute.attrelid = relation.oid AND attribute.attnum = keys.local_key
JOIN pg_attribute foreign_attribute ON foreign_attribute.attrelid = constraint_row.confrelid
    AND foreign_attribute.attnum = keys.foreign_key
WHERE constraint_row.contype = 'f' AND constraint_row.confrelid = 'secrets'::regclass
    AND foreign_attribute.attname = 'id'
ORDER BY 1`)
	require.NoError(t, err)
	defer rows.Close()
	var references []string
	for rows.Next() {
		var reference string
		require.NoError(t, rows.Scan(&reference))
		references = append(references, reference)
	}
	require.NoError(t, rows.Err())
	// New durable consumers require a branch in ListReferencedSecretIDs. The last
	// three entries and the MCP discovery cache are secret-owned children,
	// destroyed with versions rather than guarded.
	require.Equal(t, []string{
		"event_webhooks.signing_secret_id",
		"integrations.credential_secret_id",
		"machine_pools.provider_auth_secret_id",
		"mcp_server_catalogs.secret_id",
		"model_provider_configs.credential_secret_id",
		"model_provider_header_secrets.secret_id",
		"secret_grants.secret_id",
		"secret_oauth_refresh_leases.secret_id",
		"secret_versions.secret_id",
	}, references)
}
