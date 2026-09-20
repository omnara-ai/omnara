//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func newAppRuntimeFixture(
	t *testing.T,
) (inboxFixture, *secretstore.Store, integrationstore.ProjectAppRecord, uuid.UUID) {
	t.Helper()
	f := newInboxFixture(t)
	f.exec(t, `INSERT INTO org_memberships(org_id,user_id,role,created_at)
 VALUES($1,$2,'owner',now()) ON CONFLICT DO NOTHING`, f.org, f.user)
	wrapper, err := secrets.NewLocalKeyWrapper("runtime-test", map[string][]byte{
		"runtime-test": []byte("0123456789abcdef0123456789abcdef"),
	})
	require.NoError(t, err)
	secretStore := secretstore.New(f.pool, wrapper, identitystore.New(f.pool, wrapper, nil))
	secret, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
		Name: "runtime", Actor: identitystore.NewUserPrincipal(f.user),
		Material: secrets.GenericMaterial{Value: "bot-token"},
	})
	require.NoError(t, err)
	app, err := f.store.CreateProjectApp(f.ctx, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: "discord", DefinitionID: appdefinition.Discord,
	})
	require.NoError(t, err)
	app, err = f.store.ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
		OrgID: f.org, ProjectID: f.project, AppID: app.ID, InstalledByUserID: f.user,
		Provider: integrationstore.IntegrationProviderDiscord, ProviderTenantID: "123", ProviderAccountRef: "456",
		CredentialSecretID: secret.ID, CredentialVersionID: version.ID, ExpectedSetupRevision: app.SetupRevision,
	})
	require.NoError(t, err)
	f.appID = app.ID
	return f, secretStore, app, version.ID
}

func TestAppRuntimeFencesOwnershipAndCommitsReceiptWithCheckpoint(t *testing.T) {
	for _, scenario := range []string{"commit", "rollback", "expired", "rotation", "disable", "release"} {
		t.Run(scenario, func(t *testing.T) {
			f, secretStore, app, versionID := newAppRuntimeFixture(t)
			revision := integrationstore.AppRuntimeRevision{
				ProjectID:           f.project,
				AppID:               app.ID,
				Key:                 "discord/shard/0",
				SetupRevision:       app.SetupRevision,
				CredentialVersionID: versionID,
			}
			claim, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.True(t, found)
			require.Empty(t, claim.Checkpoint)
			_, found, err = f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.False(t, found)
			if scenario == "commit" {
				// Discovery on another worker must not queue behind the active
				// owner's checkpoint/heartbeat transaction.
				owner := integrationdb.BeginTx(t, f.ctx, f.pool)
				_, err := owner.Exec(
					f.ctx,
					`SELECT runtime_key FROM app_runtime WHERE app_id=$1 FOR UPDATE`,
					app.ID,
				)
				require.NoError(t, err)
				probeCtx, cancel := context.WithTimeout(f.ctx, time.Second)
				_, found, err := f.store.ClaimAppRuntime(probeCtx, revision, 30*time.Second)
				cancel()
				require.NoError(t, err)
				require.False(t, found)
				require.NoError(t, owner.Rollback(f.ctx))
			}
			require.NoError(t, f.store.RenewAppRuntime(f.ctx, claim.Lease, 30*time.Second))
			receipt := integrationstore.VerifiedIntegrationReceipt{
				ProjectID:  f.project,
				AppID:      app.ID,
				ReceiptKey: "discord:789",
				Payload:    []byte(`{"type":"MESSAGE_CREATE"}`),
			}
			checkpoint := json.RawMessage(`{"session_id":"session","sequence":7}`)
			switch scenario {
			case "rollback":
				f.exec(t, `CREATE FUNCTION reject_runtime_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
      IF NEW.checkpoint IS NOT NULL THEN RAISE EXCEPTION 'injected checkpoint failure'; END IF; RETURN NEW; END $$;
      CREATE TRIGGER reject_runtime_checkpoint BEFORE UPDATE ON app_runtime
      FOR EACH ROW EXECUTE FUNCTION reject_runtime_checkpoint()`)
			case "expired":
				f.exec(
					t,
					`UPDATE app_runtime SET claim_expires_at=now()-interval '1 second' WHERE app_id=$1`,
					app.ID,
				)
			case "rotation":
				_, _, err := secretStore.CreateSecretVersion(
					f.ctx,
					secretstore.CreateSecretVersionInput{
						OrgID:    f.org,
						SecretID: app.CredentialSecretID,
						Actor:    identitystore.NewUserPrincipal(f.user),
						Material: secrets.GenericMaterial{Value: "rotated"},
					},
				)
				require.NoError(t, err)
			case "disable":
				_, err := f.store.DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
					ProjectID: f.project, AppID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
				})
				require.NoError(t, err)
			case "release":
				require.NoError(t, f.store.ReleaseAppRuntime(f.ctx, claim.Lease, 0, ""))
				replacement, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
				require.NoError(t, err)
				require.True(t, found)
				require.NotEqual(t, claim.Lease.Token, replacement.Lease.Token)
			}
			err = f.store.CommitAppRuntime(f.ctx, claim.Lease, checkpoint, &receipt)
			if scenario == "commit" {
				require.NoError(t, err)
				// Same provider event is idempotent even while sequence advances.
				require.NoError(
					t,
					f.store.CommitAppRuntime(f.ctx, claim.Lease, json.RawMessage(`{"sequence":8}`), &receipt),
				)
				var count int
				require.NoError(
					t,
					f.pool.QueryRow(
						f.ctx,
						`SELECT count(*) FROM integration_inbox WHERE app_id=$1`,
						app.ID,
					).
						Scan(
							&count,
						),
				)
				require.Equal(t, 1, count)
				require.NoError(t, f.store.ReleaseAppRuntime(f.ctx, claim.Lease, 0, ""))
				replacement, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
				require.NoError(t, err)
				require.True(t, found)
				require.JSONEq(t, `{"sequence":8}`, string(replacement.Checkpoint))
				require.ErrorIs(
					t,
					f.store.CommitAppRuntime(f.ctx, claim.Lease, checkpoint, &receipt),
					integrationstore.ErrAppRuntimeLeaseLost,
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
				f.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE app_id=$1`, app.ID).
					Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestAppRuntimeRechecksExpiryAfterWaiting(t *testing.T) {
	f, _, app, versionID := newAppRuntimeFixture(t)
	revision := integrationstore.AppRuntimeRevision{
		ProjectID:           f.project,
		AppID:               app.ID,
		Key:                 "shard/0",
		SetupRevision:       app.SetupRevision,
		CredentialVersionID: versionID,
	}
	claim, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	_, err = tx.Exec(
		f.ctx,
		`UPDATE app_runtime SET claim_expires_at=now()-interval '1 second' WHERE app_id=$1`,
		app.ID,
	)
	require.NoError(t, err)
	done := integrationdb.RunAsyncError(
		func() error { return f.store.RenewAppRuntime(f.ctx, claim.Lease, 30*time.Second) },
	)
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockAppRuntime", 1)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(
		t,
		integrationdb.Await(t, done, "renew expired runtime"),
		integrationstore.ErrAppRuntimeLeaseLost,
	)
	wrong := claim.Lease
	wrong.AppID = uuid.New()
	require.Error(t, f.store.CommitAppRuntime(f.ctx, wrong, nil, nil))
}

func TestAppRuntimeSettingsPreserveLeaseCheckpointAndBackoff(t *testing.T) {
	f, _, app, versionID := newAppRuntimeFixture(t)
	revision := integrationstore.AppRuntimeRevision{
		ProjectID: f.project, AppID: app.ID, Key: "shard/0",
		SetupRevision: app.SetupRevision, CredentialVersionID: versionID,
	}
	claim, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	checkpoint := json.RawMessage(`{"session_id":"resume","sequence":9}`)
	require.NoError(t, f.store.CommitAppRuntime(f.ctx, claim.Lease, checkpoint, nil))
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1`, f.project).Scan(&profileID),
	)
	updated, err := f.store.UpdateProjectApp(f.ctx, app.ID, integrationstore.SaveProjectAppInput{
		OrgID: f.org, ProjectID: f.project, Name: app.Name, DefinitionID: app.DefinitionID,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention", ScopeKind: "guild", ScopeRef: "789",
			Slots: []integrationstore.AppLaunchSlot{{Key: "default", AgentProfileID: &profileID}},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, app.SetupRevision, updated.SetupRevision)
	require.True(t, updated.UpdatedAt.After(app.UpdatedAt))
	require.NoError(t, f.store.RenewAppRuntime(f.ctx, claim.Lease, 30*time.Second))
	require.NoError(t, f.store.ReleaseAppRuntime(f.ctx, claim.Lease, time.Hour, "retry later"))
	_, found, err = f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.False(t, found, "settings edits must not bypass transport backoff")
	f.exec(t, `UPDATE app_runtime SET available_at=now() WHERE app_id=$1`, app.ID)
	resumed, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, string(checkpoint), string(resumed.Checkpoint))
	require.NoError(t, f.store.ReleaseAppRuntime(f.ctx, resumed.Lease, time.Hour, "retry later"))
	f.exec(t, `UPDATE project_apps SET setup_revision=setup_revision+1 WHERE id=$1`, app.ID)
	revision.SetupRevision++
	replaced, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found, "a changed setup can bypass backoff")
	require.Empty(t, replaced.Checkpoint, "old setup checkpoints must not be reused")
}
