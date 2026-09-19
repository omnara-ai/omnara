//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestConnectionRuntimeFencesOwnershipAndCommitsReceiptWithCheckpoint(t *testing.T) {
	for _, scenario := range []string{"commit", "rollback", "expired", "rotation", "disable", "release"} {
		t.Run(scenario, func(t *testing.T) {
			f, secretStore, input := connectionFixture(t)
			secret, version, err := secretStore.CreateSecret(
				f.ctx,
				secretstore.CreateSecretInput{
					OrgID:          f.org,
					OwnerKind:      secretstore.SecretOwnerProject,
					OwnerProjectID: f.project,
					Name:           "discord",
					Actor:          identitystore.NewUserPrincipal(f.user),
					Material:       secrets.GenericMaterial{Value: "bot-token"},
				},
			)
			require.NoError(t, err)
			input.Provider, input.ProviderTenantID, input.ProviderAccountRef = "discord", "123", "456"
			input.CredentialSecretID = secret.ID
			connection, err := f.store.CreateIntegrationConnection(f.ctx, input)
			require.NoError(t, err)
			revision := integrationstore.RuntimeRevision{
				ProjectID:           f.project,
				ConnectionID:        connection.ID,
				Key:                 "discord/shard/0",
				ConnectionUpdatedAt: connection.UpdatedAt,
				CredentialVersionID: version.ID,
			}
			claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.True(t, found)
			require.Empty(t, claim.Checkpoint)
			_, found, err = f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.False(t, found)
			if scenario == "commit" {
				// Discovery on another worker must not queue behind the active
				// owner's checkpoint/heartbeat transaction.
				owner := integrationdb.BeginTx(t, f.ctx, f.pool)
				_, err := owner.Exec(
					f.ctx,
					`SELECT runtime_key FROM integration_connection_runtime WHERE connection_id=$1 FOR UPDATE`,
					connection.ID,
				)
				require.NoError(t, err)
				probeCtx, cancel := context.WithTimeout(f.ctx, time.Second)
				_, found, err := f.store.ClaimIntegrationRuntime(probeCtx, revision, 30*time.Second)
				cancel()
				require.NoError(t, err)
				require.False(t, found)
				require.NoError(t, owner.Rollback(f.ctx))
			}
			require.NoError(t, f.store.RenewIntegrationRuntime(f.ctx, claim.Lease, 30*time.Second))
			receipt := integrationstore.VerifiedIntegrationReceipt{
				ProjectID:    f.project,
				ConnectionID: connection.ID,
				ReceiptKey:   "discord:789",
				Payload:      []byte(`{"type":"MESSAGE_CREATE"}`),
			}
			checkpoint := json.RawMessage(`{"session_id":"session","sequence":7}`)
			switch scenario {
			case "rollback":
				f.exec(t, `CREATE FUNCTION reject_runtime_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
      IF NEW.checkpoint IS NOT NULL THEN RAISE EXCEPTION 'injected checkpoint failure'; END IF; RETURN NEW; END $$;
      CREATE TRIGGER reject_runtime_checkpoint BEFORE UPDATE ON integration_connection_runtime
      FOR EACH ROW EXECUTE FUNCTION reject_runtime_checkpoint()`)
			case "expired":
				f.exec(
					t,
					`UPDATE integration_connection_runtime SET claim_expires_at=now()-interval '1 second' WHERE connection_id=$1`,
					connection.ID,
				)
			case "rotation":
				_, _, err := secretStore.CreateSecretVersion(
					f.ctx,
					secretstore.CreateSecretVersionInput{
						OrgID:    f.org,
						SecretID: secret.ID,
						Actor:    identitystore.NewUserPrincipal(f.user),
						Material: secrets.GenericMaterial{Value: "rotated"},
					},
				)
				require.NoError(t, err)
			case "disable":
				_, err := f.store.DisableIntegrationConnection(
					f.ctx,
					integrationstore.DisableIntegrationConnectionInput{
						ProjectID:           f.project,
						ID:                  connection.ID,
						ExpectedOAuthFlowID: &connection.LastOAuthFlowID,
					},
				)
				require.NoError(t, err)
			case "release":
				require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claim.Lease, 0, ""))
				replacement, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
				require.NoError(t, err)
				require.True(t, found)
				require.NotEqual(t, claim.Lease.Token, replacement.Lease.Token)
			}
			err = f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, checkpoint, &receipt)
			if scenario == "commit" {
				require.NoError(t, err)
				// Same provider event is idempotent even while sequence advances.
				require.NoError(
					t,
					f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, json.RawMessage(`{"sequence":8}`), &receipt),
				)
				var count int
				require.NoError(
					t,
					f.pool.QueryRow(
						f.ctx,
						`SELECT count(*) FROM integration_inbox WHERE connection_id=$1`,
						connection.ID,
					).
						Scan(
							&count,
						),
				)
				require.Equal(t, 1, count)
				require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claim.Lease, 0, ""))
				replacement, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
				require.NoError(t, err)
				require.True(t, found)
				require.JSONEq(t, `{"sequence":8}`, string(replacement.Checkpoint))
				require.ErrorIs(
					t,
					f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, checkpoint, &receipt),
					integrationstore.ErrIntegrationRuntimeLeaseLost,
				)
				return
			}
			require.Error(t, err)
			if scenario == "rollback" {
				require.ErrorContains(t, err, "injected checkpoint failure")
			}
			var count int
			require.NoError(
				t,
				f.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE connection_id=$1`, connection.ID).
					Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestConnectionRuntimeRechecksExpiryAfterWaiting(t *testing.T) {
	f, secretStore, input := connectionFixture(t)
	secret, version, err := secretStore.CreateSecret(
		f.ctx,
		secretstore.CreateSecretInput{
			OrgID:          f.org,
			OwnerKind:      secretstore.SecretOwnerProject,
			OwnerProjectID: f.project,
			Name:           "runtime",
			Actor:          identitystore.NewUserPrincipal(f.user),
			Material:       secrets.GenericMaterial{Value: "token"},
		},
	)
	require.NoError(t, err)
	input.Provider, input.ProviderTenantID, input.ProviderAccountRef = "discord", "123", "456"
	input.CredentialSecretID = secret.ID
	connection, err := f.store.CreateIntegrationConnection(f.ctx, input)
	require.NoError(t, err)
	revision := integrationstore.RuntimeRevision{
		ProjectID:           f.project,
		ConnectionID:        connection.ID,
		Key:                 "shard/0",
		ConnectionUpdatedAt: connection.UpdatedAt,
		CredentialVersionID: version.ID,
	}
	claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	_, err = tx.Exec(
		f.ctx,
		`UPDATE integration_connection_runtime SET claim_expires_at=now()-interval '1 second' WHERE connection_id=$1`,
		connection.ID,
	)
	require.NoError(t, err)
	done := integrationdb.RunAsyncError(
		func() error { return f.store.RenewIntegrationRuntime(f.ctx, claim.Lease, 30*time.Second) },
	)
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationConnectionRuntime", 1)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(
		t,
		integrationdb.Await(t, done, "renew expired runtime"),
		integrationstore.ErrIntegrationRuntimeLeaseLost,
	)
	wrong := claim.Lease
	wrong.ConnectionID = uuid.New()
	require.Error(t, f.store.CommitIntegrationRuntime(f.ctx, wrong, nil, nil))
}
