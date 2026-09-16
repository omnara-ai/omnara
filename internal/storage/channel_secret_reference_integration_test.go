//go:build integration

package storage

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

// Credential admission must retain its own secret lock after the channel
// cutover removes the duplicate model-config and machine-pool triggers.
func TestChannelCutoverCredentialWritersSerializeWithSecretDeletion(t *testing.T) {
	t.Parallel()
	for _, resource := range []string{"model", "pool"} {
		for _, update := range []bool{false, true} {
			for _, referenceWins := range []bool{false, true} {
				operation, winner := "create", "deletion_wins"
				if update {
					operation = "update"
				}
				if referenceWins {
					winner = "reference_wins"
				}
				t.Run(resource+"/"+operation+"/"+winner, func(t *testing.T) {
					t.Parallel()
					ctx := t.Context()
					pool := openIntegrationDB(t, ctx)
					seedMigratedDB(t, ctx, pool)
					store := newSecretIntegrationStore(pool)
					admin := createSecretTestUser(t, ctx, store, "Credential Writer Race", "admin")
					createSecret := func(name string) (secretstore.SecretRecord, secretstore.SecretVersionRecord) {
						t.Helper()
						secret, version, err := store.Secrets().CreateSecret(ctx, secretstore.CreateSecretInput{
							OrgID: testOrgID, OwnerKind: secretstore.SecretOwnerOrg, Name: name,
							Material: secrets.GenericMaterial{Value: "local-fixture"}, Actor: userPrincipal(admin.ID),
						})
						require.NoError(t, err)
						return secret, version
					}
					secret, version := createSecret("raced credential")
					var recordID uuid.UUID
					create := func(ctx context.Context, secretID uuid.UUID) (uuid.UUID, error) {
						if resource == "pool" {
							record, err := createMachinePoolReferencingSecretForTest(ctx, store, "raced pool", secretID)
							return record.ID, err
						}
						record, err := store.Models().CreateModelProviderConfig(ctx, modelstore.CreateModelProviderConfigInput{
							OrgID: testOrgID, Name: "raced model", APIFormat: modelprotocol.APIFormatOpenAIResponses,
							BaseURL: "https://api.openai.com/v1", CredentialSecretID: secretID,
						})
						return record.ID, err
					}
					var originalSecretID uuid.UUID
					if update {
						original, _ := createSecret("original credential")
						originalSecretID = original.ID
						var err error
						recordID, err = create(ctx, original.ID)
						require.NoError(t, err)
					}
					write := func() error {
						if !update {
							_, err := create(ctx, secret.ID)
							return err
						}
						if resource == "pool" {
							_, err := store.Execution().UpdateMachinePool(ctx, executionstore.UpdateMachinePoolInput{
								OrgID: testOrgID, ID: recordID, ProviderAuthSecretID: &secret.ID,
							})
							return err
						}
						_, err := store.Models().PatchModelProviderConfig(ctx, modelstore.PatchModelProviderConfigInput{
							OrgID: testOrgID, ID: recordID, CredentialSecretID: &secret.ID,
						})
						return err
					}
					remove := func() error {
						_, err := store.Secrets().DeleteSecretOnceForIntegration(ctx, secretstore.DeleteSecretInput{
							OrgID: testOrgID, SecretID: secret.ID, Actor: userPrincipal(admin.ID),
						})
						return err
					}
					control := integrationdb.BeginTx(t, ctx, pool)
					if referenceWins {
						// SHARE permits the writer's initial row read/lock, but holds
						// INSERT/UPDATE after the production secret lock was acquired.
						table, query := "model_provider_configs", "InsertModelProviderConfig"
						if resource == "pool" {
							table, query = "machine_pools", "InsertMachinePool"
						}
						if update {
							query = "Update" + query[len("Insert"):]
						}
						_, err := control.Exec(ctx, "LOCK TABLE "+table+" IN SHARE MODE")
						require.NoError(t, err)
						written := integrationdb.RunAsyncError(write)
						integrationdb.WaitForNamedLockWaiters(t, ctx, pool, query, 1)
						deleted := integrationdb.RunAsyncError(remove)
						integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockSecret", 1)
						require.NoError(t, control.Commit(ctx))
						require.NoError(t, integrationdb.Await(t, written, "credential writer"))
						require.ErrorIs(t, integrationdb.Await(t, deleted, "referenced secret deletion"), storeerr.ErrConflict)
					} else {
						_, err := control.Exec(ctx, `SELECT id FROM secret_versions WHERE id = $1 FOR UPDATE`, version.ID)
						require.NoError(t, err)
						deleted := integrationdb.RunAsyncError(remove)
						integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "DeleteSecretVersions", 1)
						written := integrationdb.RunAsyncError(write)
						integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockSecretForReference", 1)
						require.NoError(t, control.Commit(ctx))
						require.NoError(t, integrationdb.Await(t, deleted, "secret deletion"))
						require.ErrorIs(t, integrationdb.Await(t, written, "writer after deletion"), storeerr.ErrNotFound)
					}
					var references int
					require.NoError(t, pool.QueryRow(ctx, `SELECT
  (SELECT count(*) FROM model_provider_configs WHERE credential_secret_id = $1) +
  (SELECT count(*) FROM machine_pools WHERE provider_auth_secret_id = $1)`, secret.ID).Scan(&references))
					if referenceWins {
						require.Equal(t, 1, references)
					} else {
						require.Zero(t, references)
						if update {
							var preserved bool
							require.NoError(t, pool.QueryRow(ctx, `SELECT
  EXISTS (SELECT 1 FROM model_provider_configs WHERE id = $1 AND credential_secret_id = $2) OR
  EXISTS (SELECT 1 FROM machine_pools WHERE id = $1 AND provider_auth_secret_id = $2)`,
								recordID, originalSecretID).Scan(&preserved))
							require.True(t, preserved, "failed reassociation preserves the original credential")
						}
					}
				})
			}
		}
	}
}
