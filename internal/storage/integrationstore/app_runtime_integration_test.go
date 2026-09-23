//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPersistentAppsMergeTypesAndPageByIdentity(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	var want, discordIDs []uuid.UUID
	for i := range 9 {
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1))
		appType := appdefinition.DiscordThread
		if i%2 == 1 {
			appType = appdefinition.GitHubPR
		}
		if i == 8 {
			appType = appdefinition.SlackThread
		}
		f.exec(t, `INSERT INTO project_apps
 (id,org_id,project_id,name,app_type,state,installed_by_user_id,credential_secret_id,
  provider_tenant_id,provider_account_ref,created_at,updated_at)
 SELECT $1,org_id,project_id,$2,$3,'active',installed_by_user_id,credential_secret_id,
        provider_tenant_id,provider_account_ref,now(),now()
 FROM project_apps WHERE id=$4`, id, fmt.Sprintf("runtime-%d", i), string(appType), f.appID)
		switch i {
		case 3:
			f.exec(t, `UPDATE project_apps SET state='disconnected' WHERE id=$1`, id)
		case 5:
			f.exec(t, `UPDATE project_apps SET deleted_at=now() WHERE id=$1`, id)
		case 8:
		default:
			want = append(want, id)
			if appType == appdefinition.DiscordThread {
				discordIDs = append(discordIDs, id)
			}
		}
	}
	query := dbsqlc.New(f.pool)
	for _, appTypes := range [][]string{
		{"discord_thread", "github_pr"},
		{"github_pr", "discord_thread", "github_pr"},
	} {
		for _, limit := range []int32{1, 3, 4} {
			var after *uuid.UUID
			var got []uuid.UUID
			for page := 0; ; page++ {
				require.Less(t, page, len(want)+1, "cursor must make progress")
				rows, err := query.ListPersistentApps(f.ctx, dbsqlc.ListPersistentAppsParams{
					AppTypes: appTypes, AfterID: after, RowLimit: limit,
				})
				require.NoError(t, err)
				require.LessOrEqual(t, len(rows), int(limit), "the merged page respects the global limit")
				for _, row := range rows {
					require.Equal(t, f.project, row.ProjectID)
					got = append(got, row.ID)
				}
				if len(rows) == 0 {
					break
				}
				after = &rows[len(rows)-1].ID
			}
			require.Equal(t, want, got, "types=%v page size=%d", appTypes, limit)
		}
	}
	for _, appTypes := range [][]string{nil, {}} {
		rows, err := query.ListPersistentApps(f.ctx, dbsqlc.ListPersistentAppsParams{AppTypes: appTypes, RowLimit: 3})
		require.NoError(t, err)
		require.Empty(t, rows, "an empty transport registration cannot discover apps")
	}
	var got []uuid.UUID
	var after uuid.UUID
	for page := 0; ; page++ {
		require.Less(t, page, len(discordIDs)+1)
		rows, err := f.store.ListPersistentApps(f.ctx, after, 2)
		require.NoError(t, err)
		for _, row := range rows {
			got = append(got, row.AppID)
		}
		if len(rows) == 0 {
			break
		}
		after = rows[len(rows)-1].AppID
	}
	require.Equal(t, discordIDs, got, "the store supplies the persistent transport's registered types")
	f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
	rows, err := query.ListPersistentApps(f.ctx, dbsqlc.ListPersistentAppsParams{
		AppTypes: []string{"discord_thread", "github_pr"}, RowLimit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, rows, "deleted projects cannot host persistent runtimes")
}

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
		OrgID: f.org, ProjectID: f.project, Name: "discord", AppType: appdefinition.DiscordThread,
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

func TestAppRuntimeFailureUsesCurrentSetupAndCredential(t *testing.T) {
	for _, scenario := range []string{
		"failed", "retry_due", "reclaimed", "stale_response", "setup_changed", "rotated",
		"secret_deleted", "credential_unavailable", "disconnected", "app_deleted", "project_deleted", "org_deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			f, secretStore, app, versionID := newAppRuntimeFixture(t)
			if scenario == "credential_unavailable" {
				secret, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
					OrgID: f.org, OwnerKind: secretstore.SecretOwnerOrg, Name: "shared-runtime",
					Actor: identitystore.NewUserPrincipal(f.user), Material: secrets.GenericMaterial{Value: "bot-token"},
				})
				require.NoError(t, err)
				f.exec(t, `INSERT INTO secret_grants(org_id,secret_id,target_project_id,created_at)
                    VALUES($1,$2,$3,now())`, f.org, secret.ID, f.project)
				app, err = f.store.ConfigureProjectApp(f.ctx, integrationstore.ConfigureProjectAppInput{
					OrgID: f.org, ProjectID: f.project, AppID: app.ID, InstalledByUserID: f.user,
					Provider: app.Provider, ProviderTenantID: app.ProviderTenantID, ProviderAccountRef: app.ProviderAccountRef,
					CredentialSecretID: secret.ID, CredentialVersionID: version.ID, ExpectedSetupRevision: app.SetupRevision,
				})
				require.NoError(t, err)
				versionID = version.ID
			}
			read := func() *integrationstore.AppRuntimeFailure {
				t.Helper()
				failure, err := f.store.GetAppRuntimeFailure(f.ctx, f.project, app.ID, app.SetupRevision)
				if errors.Is(err, storeerr.ErrNotFound) {
					return nil
				}
				require.NoError(t, err)
				return &failure
			}
			require.Nil(t, read())
			revision := integrationstore.AppRuntimeRevision{
				ProjectID: f.project, AppID: app.ID, Key: "discord/shard/0",
				SetupRevision: app.SetupRevision, CredentialVersionID: versionID,
			}
			claim, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.True(t, found)
			require.Nil(t, read(), "a lease does not report a provider connection state")
			started := time.Now()
			require.NoError(t, f.store.ReleaseAppRuntime(f.ctx, claim.Lease, time.Hour, "Discord Gateway closed: 4014"))
			failure := read()
			require.NotNil(t, failure)
			require.Equal(t, "Discord Gateway closed: 4014", failure.Message)
			require.WithinDuration(t, started.Add(time.Hour), failure.RetryAt, 5*time.Second)
			wrongProject, err := f.store.GetAppRuntimeFailure(f.ctx, uuid.New(), app.ID, app.SetupRevision)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			require.Zero(t, wrongProject)
			switch scenario {
			case "retry_due", "reclaimed":
				f.exec(t, `UPDATE app_runtime SET available_at=now()-interval '1 second' WHERE app_id=$1`, app.ID)
				if scenario == "reclaimed" {
					_, found, err := f.store.ClaimAppRuntime(f.ctx, revision, 30*time.Second)
					require.NoError(t, err)
					require.True(t, found)
				}
			case "stale_response", "setup_changed":
				f.exec(t, `UPDATE project_apps SET setup_revision=setup_revision+1 WHERE id=$1`, app.ID)
				if scenario == "setup_changed" {
					app.SetupRevision++
				}
			case "rotated":
				_, _, err := secretStore.CreateSecretVersion(f.ctx, secretstore.CreateSecretVersionInput{
					OrgID: f.org, SecretID: app.CredentialSecretID,
					Actor: identitystore.NewUserPrincipal(f.user), Material: secrets.GenericMaterial{Value: "rotated"},
				})
				require.NoError(t, err)
			case "secret_deleted":
				f.exec(t, `UPDATE secrets SET deleted_at=now() WHERE id=$1`, app.CredentialSecretID)
			case "credential_unavailable":
				f.exec(t, `DELETE FROM secret_grants WHERE secret_id=$1 AND target_project_id=$2`,
					app.CredentialSecretID, f.project)
			case "disconnected":
				_, err := f.store.DisconnectProjectApp(f.ctx, integrationstore.DisconnectProjectAppInput{
					ProjectID: f.project, AppID: app.ID, ExpectedSetupRevision: &app.SetupRevision,
				})
				require.NoError(t, err)
			case "app_deleted":
				f.exec(t, `UPDATE project_apps SET deleted_at=now() WHERE id=$1`, app.ID)
			case "project_deleted":
				f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
			case "org_deleted":
				f.exec(t, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.org)
			}
			if scenario == "failed" || scenario == "retry_due" {
				require.NotNil(t, read(), "failure remains visible until the next attempt")
			} else {
				require.Nil(t, read(), "stale or unavailable setup must not report a failure")
			}
		})
	}
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
		OrgID: f.org, ProjectID: f.project, Name: app.Name, AppType: app.AppType,
		Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
			Trigger: "mention",
			Slots:   []integrationstore.AppLaunchSlot{{Key: "default", AgentProfileID: &profileID}},
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
