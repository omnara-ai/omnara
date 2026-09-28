//go:build integration

package integrationstore_test

import (
"github.com/omnara-ai/omnara/internal/testutil/integrationtest"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestPersistentIntegrationsMergeTypesAndPageByIdentity(t *testing.T) {
	t.Parallel()
	f := newInboxFixture(t)
	var want, discordIDs []uuid.UUID
	for i := range 9 {
		id := uuid.MustParse(fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1))
		integrationKind := integrationdefinition.DiscordThread
		if i%2 == 1 {
			integrationKind = integrationdefinition.GitHubPR
		}
		if i == 8 {
			integrationKind = integrationdefinition.SlackThread
		}
		f.exec(t, `INSERT INTO integrations
 (id,org_id,project_id,name,integration_kind,state,installed_by_user_id,credential_secret_id,
  provider_tenant_id,provider_account_ref,created_at,updated_at)
 SELECT $1,org_id,project_id,$2,$3,'active',installed_by_user_id,credential_secret_id,
        provider_tenant_id,provider_account_ref,now(),now()
 FROM integrations WHERE id=$4`, id, fmt.Sprintf("runtime-%d", i), string(integrationKind), f.integrationID)
		switch i {
		case 3:
			f.exec(t, `UPDATE integrations SET state='disconnected' WHERE id=$1`, id)
		case 5:
			f.exec(t, `UPDATE integrations SET deleted_at=now() WHERE id=$1`, id)
		case 8:
		default:
			want = append(want, id)
			if integrationKind == integrationdefinition.DiscordThread {
				discordIDs = append(discordIDs, id)
			}
		}
	}
	query := dbsqlc.New(f.pool)
	for _, integrationKinds := range [][]string{
		{"discord_thread", "github_pr"},
		{"github_pr", "discord_thread", "github_pr"},
	} {
		for _, limit := range []int32{1, 3, 4} {
			var after *uuid.UUID
			var got []uuid.UUID
			for page := 0; ; page++ {
				require.Less(t, page, len(want)+1, "cursor must make progress")
				rows, err := query.ListPersistentIntegrations(f.ctx, dbsqlc.ListPersistentIntegrationsParams{
					IntegrationKinds: integrationKinds, AfterID: after, RowLimit: limit,
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
			require.Equal(t, want, got, "types=%v page size=%d", integrationKinds, limit)
		}
	}
	for _, integrationKinds := range [][]string{nil, {}} {
		rows, err := query.ListPersistentIntegrations(
			f.ctx,
			dbsqlc.ListPersistentIntegrationsParams{IntegrationKinds: integrationKinds, RowLimit: 3},
		)
		require.NoError(t, err)
		require.Empty(t, rows, "an empty transport registration cannot discover integrations")
	}
	var got []uuid.UUID
	var after uuid.UUID
	for page := 0; ; page++ {
		require.Less(t, page, len(discordIDs)+1)
		rows, err := f.store.ListPersistentIntegrations(f.ctx, after, 2)
		require.NoError(t, err)
		for _, row := range rows {
			got = append(got, row.IntegrationID)
		}
		if len(rows) == 0 {
			break
		}
		after = rows[len(rows)-1].IntegrationID
	}
	require.Equal(t, discordIDs, got, "the store supplies the persistent transport's registered types")
	f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
	rows, err := query.ListPersistentIntegrations(f.ctx, dbsqlc.ListPersistentIntegrationsParams{
		IntegrationKinds: []string{"discord_thread", "github_pr"}, RowLimit: 10,
	})
	require.NoError(t, err)
	require.Empty(t, rows, "deleted projects cannot host persistent runtimes")
}

func newIntegrationRuntimeFixture(
	t *testing.T,
) (inboxFixture, *secretstore.Store, integrationstore.IntegrationRecord, uuid.UUID) {
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
	integration, err := f.store.CreateIntegration(f.ctx, integrationstore.SaveIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: "discord", IntegrationKind: integrationdefinition.DiscordThread,
	})
	require.NoError(t, err)
	integration, err = f.store.ConfigureIntegration(f.ctx, integrationstore.ConfigureIntegrationInput{
		OrgID: f.org, ProjectID: f.project, IntegrationID: integration.ID, InstalledByUserID: f.user,
		Provider: integrationstore.IntegrationProviderDiscord, ProviderTenantID: "123", ProviderAccountRef: "456",
		CredentialSecretID: secret.ID, CredentialVersionID: version.ID, ExpectedSetupRevision: integration.SetupRevision,
	})
	require.NoError(t, err)
	f.integrationID = integration.ID
	return f, secretStore, integration, version.ID
}

func TestUnclaimedDiscordIntegrations(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"new", "claimed", "expired", "retry_wait", "disconnected", "deleted", "project_deleted", "org_deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			f, _, integration, version := newIntegrationRuntimeFixture(t)
			want := int64(1)
			switch scenario {
			case "claimed", "expired", "retry_wait":
				claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, integrationstore.IntegrationRuntimeRevision{
					ProjectID: f.project, IntegrationID: integration.ID,
					SetupRevision: integration.SetupRevision, CredentialVersionID: version,
				}, time.Minute)
				require.NoError(t, err)
				require.True(t, found)
				switch scenario {
				case "claimed":
					want = 0
				case "expired":
					f.exec(t, `UPDATE integration_runtime SET claim_expires_at=now()-interval '1 second'
WHERE integration_id=$1`, integration.ID)
				case "retry_wait":
					require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claim.Lease, time.Hour, "retrying"))
				}
			case "disconnected":
				f.exec(t, `UPDATE integrations SET state='disconnected' WHERE id=$1`, integration.ID)
				want = 0
			case "deleted":
				f.exec(t, `UPDATE integrations SET deleted_at=now() WHERE id=$1`, integration.ID)
				want = 0
			case "project_deleted":
				f.exec(t, `UPDATE projects SET deleted_at=now() WHERE id=$1`, f.project)
				want = 0
			case "org_deleted":
				f.exec(t, `UPDATE orgs SET deleted_at=now() WHERE id=$1`, f.org)
				want = 0
			}
			count, err := f.store.CountUnclaimedDiscordIntegrations(f.ctx)
			require.NoError(t, err)
			require.Equal(t, want, count)
		})
	}
}

func TestIntegrationRuntimeIsolatesIntegrationsSharingBot(t *testing.T) {
	t.Parallel()
	f, _, first, versionID := newIntegrationRuntimeFixture(t)
	second, err := f.store.CreateIntegration(f.ctx, integrationstore.SaveIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: "other-discord", IntegrationKind: integrationdefinition.DiscordThread,
	})
	require.NoError(t, err)
	second, err = f.store.ConfigureIntegration(f.ctx, integrationstore.ConfigureIntegrationInput{
		OrgID: f.org, ProjectID: f.project, IntegrationID: second.ID, InstalledByUserID: f.user,
		Provider: first.Provider, ProviderTenantID: first.ProviderTenantID, ProviderAccountRef: first.ProviderAccountRef,
		CredentialSecretID: first.CredentialSecretID, CredentialVersionID: versionID,
		ExpectedSetupRevision: second.SetupRevision,
	})
	require.NoError(t, err)
	var claims []integrationstore.IntegrationRuntimeClaim
	for i, integration := range []integrationstore.IntegrationRecord{first, second} {
		revision := integrationstore.IntegrationRuntimeRevision{
			ProjectID: f.project, IntegrationID: integration.ID,
			SetupRevision: integration.SetupRevision, CredentialVersionID: versionID,
		}
		claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, time.Minute)
		require.NoError(t, err)
		require.True(t, found)
		claims = append(claims, claim)
		_, found, err = f.store.ClaimIntegrationRuntime(f.ctx, revision, time.Minute)
		require.NoError(t, err)
		require.False(t, found, "each integration has only one runtime owner")
		count, err := f.store.CountUnclaimedDiscordIntegrations(f.ctx)
		require.NoError(t, err)
		require.Equal(t, int64(1-i), count, "another integration sharing the bot still needs its own claim")
		checkpoint := json.RawMessage(fmt.Sprintf(`{"sequence":%d}`, i+1))
		require.NoError(t, f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, checkpoint, nil))
	}
	wrong := claims[0].Lease
	wrong.IntegrationID = second.ID
	require.ErrorIs(t, f.store.RenewIntegrationRuntime(f.ctx, wrong, time.Minute),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.ErrorIs(t, f.store.CommitIntegrationRuntime(f.ctx, wrong, nil, nil),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.ErrorIs(t, f.store.ReleaseIntegrationRuntime(f.ctx, wrong, 0, ""),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claims[0].Lease, time.Hour, "retry first"))
	require.NoError(t, f.store.RenewIntegrationRuntime(f.ctx, claims[1].Lease, time.Minute))
	count, err := f.store.CountUnclaimedDiscordIntegrations(f.ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	_, found, err := f.store.ClaimIntegrationRuntime(f.ctx, claims[0].Lease.IntegrationRuntimeRevision, time.Minute)
	require.NoError(t, err)
	require.False(t, found, "the first integration retains its own backoff")
	require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claims[1].Lease, 0, ""))
	resumed, found, err := f.store.ClaimIntegrationRuntime(f.ctx, claims[1].Lease.IntegrationRuntimeRevision, time.Minute)
	require.NoError(t, err)
	require.True(t, found, "a sibling's backoff must not block this integration")
	require.JSONEq(t, `{"sequence":2}`, string(resumed.Checkpoint))
	f.exec(t, `UPDATE integration_runtime SET next_attempt_at=now() WHERE integration_id=$1`, first.ID)
	resumed, found, err = f.store.ClaimIntegrationRuntime(f.ctx, claims[0].Lease.IntegrationRuntimeRevision, time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, `{"sequence":1}`, string(resumed.Checkpoint))
}

func TestIntegrationRuntimeRejectsWrongSecretVersion(t *testing.T) {
	t.Parallel()
	f, secretStore, integration, versionID := newIntegrationRuntimeFixture(t)
	_, otherVersion, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
		OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
		Name: "other-runtime", Actor: identitystore.NewUserPrincipal(f.user),
		Material: secrets.GenericMaterial{Value: "other-bot-token"},
	})
	require.NoError(t, err)
	revision := integrationstore.IntegrationRuntimeRevision{
		ProjectID: f.project, IntegrationID: integration.ID,
		SetupRevision: integration.SetupRevision, CredentialVersionID: versionID,
	}
	wrong := revision
	wrong.CredentialVersionID = otherVersion.ID
	_, found, err := f.store.ClaimIntegrationRuntime(f.ctx, wrong, time.Minute)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.False(t, found, "an existing version from another available secret cannot create a runtime")
	claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	checkpoint := json.RawMessage(`{"session_id":"authorized","sequence":7}`)
	require.NoError(t, f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, checkpoint, nil))
	wrongLease := claim.Lease
	wrongLease.CredentialVersionID = otherVersion.ID
	require.ErrorIs(t, f.store.RenewIntegrationRuntime(f.ctx, wrongLease, time.Minute),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.ErrorIs(t, f.store.CommitIntegrationRuntime(f.ctx, wrongLease, nil, nil),
		integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claim.Lease, 0, ""))
	_, found, err = f.store.ClaimIntegrationRuntime(f.ctx, wrong, time.Minute)
	require.ErrorIs(t, err, integrationstore.ErrIntegrationRuntimeLeaseLost)
	require.False(t, found, "another secret's version cannot replace an existing runtime")
	resumed, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, string(checkpoint), string(resumed.Checkpoint))
}

func TestIntegrationRuntimeReconfigureThenDeleteOldSecret(t *testing.T) {
	t.Parallel()
	for _, reclaimBeforeDelete := range []bool{false, true} {
		t.Run(fmt.Sprintf("reclaim_before_delete=%t", reclaimBeforeDelete), func(t *testing.T) {
			t.Parallel()
			f, secretStore, integration, versionID := newIntegrationRuntimeFixture(t)
			oldSecretID := integration.CredentialSecretID
			old, found, err := f.store.ClaimIntegrationRuntime(f.ctx, integrationstore.IntegrationRuntimeRevision{
				ProjectID: f.project, IntegrationID: integration.ID,
				SetupRevision: integration.SetupRevision, CredentialVersionID: versionID,
			}, time.Minute)
			require.NoError(t, err)
			require.True(t, found)
			require.NoError(t, f.store.CommitIntegrationRuntime(f.ctx, old.Lease, json.RawMessage(`{"session_id":"old"}`), nil))
			require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, old.Lease, time.Hour, "old failure"))
			secret, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
				OrgID: f.org, OwnerKind: secretstore.SecretOwnerProject, OwnerProjectID: f.project,
				Name: "replacement-runtime", Actor: identitystore.NewUserPrincipal(f.user),
				Material: secrets.GenericMaterial{Value: "replacement-bot-token"},
			})
			require.NoError(t, err)
			integration, err = f.store.ConfigureIntegration(f.ctx, integrationstore.ConfigureIntegrationInput{
				OrgID: f.org, ProjectID: f.project, IntegrationID: integration.ID, InstalledByUserID: f.user,
				Provider: integration.Provider, ProviderTenantID: integration.ProviderTenantID,
				ProviderAccountRef: integration.ProviderAccountRef,
				CredentialSecretID: secret.ID, CredentialVersionID: version.ID, ExpectedSetupRevision: integration.SetupRevision,
			})
			require.NoError(t, err)
			require.Greater(t, integration.SetupRevision, old.Lease.SetupRevision)
			revision := integrationstore.IntegrationRuntimeRevision{
				ProjectID: f.project, IntegrationID: integration.ID,
				SetupRevision: integration.SetupRevision, CredentialVersionID: version.ID,
			}
			var replacement integrationstore.IntegrationRuntimeClaim
			if reclaimBeforeDelete {
				replacement, found, err = f.store.ClaimIntegrationRuntime(f.ctx, revision, time.Minute)
				require.NoError(t, err)
				require.True(t, found, "reconfiguration bypasses the old setup's backoff")
				require.Empty(t, replacement.Checkpoint)
				require.NoError(t, f.store.CommitIntegrationRuntime(f.ctx, replacement.Lease, json.RawMessage(`{"session_id":"new"}`), nil))
			}
			_, err = secretStore.DeleteSecret(f.ctx, secretstore.DeleteSecretInput{
				OrgID: f.org, SecretID: oldSecretID, Actor: identitystore.NewUserPrincipal(f.user),
			})
			require.NoError(t, err, "a stale runtime must not block destruction of the old secret's versions")
			var remaining int
			require.NoError(t, f.pool.QueryRow(f.ctx,
				`SELECT count(*) FROM integration_runtime WHERE integration_id=$1`, integration.ID).Scan(&remaining))
			if reclaimBeforeDelete {
				require.Equal(t, 1, remaining, "deleting the old version must preserve the new runtime")
				require.NoError(t, f.store.RenewIntegrationRuntime(f.ctx, replacement.Lease, time.Minute))
				require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, replacement.Lease, 0, ""))
			} else {
				require.Zero(t, remaining, "deleting the old version cascades to its stale runtime")
			}
			require.ErrorIs(t, f.store.RenewIntegrationRuntime(f.ctx, old.Lease, time.Minute),
				integrationstore.ErrIntegrationRuntimeLeaseLost)
			require.ErrorIs(t, f.store.CommitIntegrationRuntime(f.ctx, old.Lease, nil, nil),
				integrationstore.ErrIntegrationRuntimeLeaseLost)
			replacement, found, err = f.store.ClaimIntegrationRuntime(f.ctx, revision, time.Minute)
			require.NoError(t, err)
			require.True(t, found)
			if reclaimBeforeDelete {
				require.JSONEq(t, `{"session_id":"new"}`, string(replacement.Checkpoint))
			} else {
				require.Empty(t, replacement.Checkpoint, "a deleted credential's session cannot resume")
			}
			require.NoError(t, f.store.CommitIntegrationRuntime(f.ctx, replacement.Lease, json.RawMessage(`{"sequence":1}`), nil))
		})
	}
}

func TestIntegrationRuntimeFailureUsesCurrentSetupAndCredential(t *testing.T) {
	for _, scenario := range []string{
		"failed", "retry_due", "reclaimed", "stale_response", "setup_changed", "rotated",
		"secret_deleted", "credential_unavailable", "disconnected", "integration_deleted", "project_deleted", "org_deleted",
	} {
		t.Run(scenario, func(t *testing.T) {
			f, secretStore, integration, versionID := newIntegrationRuntimeFixture(t)
			if scenario == "credential_unavailable" {
				secret, version, err := secretStore.CreateSecret(f.ctx, secretstore.CreateSecretInput{
					OrgID: f.org, OwnerKind: secretstore.SecretOwnerOrg, Name: "shared-runtime",
					Actor: identitystore.NewUserPrincipal(f.user), Material: secrets.GenericMaterial{Value: "bot-token"},
				})
				require.NoError(t, err)
				f.exec(t, `INSERT INTO secret_grants(org_id,secret_id,target_project_id,created_at)
                    VALUES($1,$2,$3,now())`, f.org, secret.ID, f.project)
				integration, err = f.store.ConfigureIntegration(f.ctx, integrationstore.ConfigureIntegrationInput{

					OrgID:             f.org,
					ProjectID:         f.project,
					IntegrationID:     integration.ID,
					InstalledByUserID: f.user,

					Provider:           integration.Provider,
					ProviderTenantID:   integration.ProviderTenantID,
					ProviderAccountRef: integration.ProviderAccountRef,

					CredentialSecretID:    secret.ID,
					CredentialVersionID:   version.ID,
					ExpectedSetupRevision: integration.SetupRevision,
				})
				require.NoError(t, err)
				versionID = version.ID
			}
			read := func() *integrationstore.IntegrationRuntimeFailure {
				t.Helper()
				failure, err := f.store.GetIntegrationRuntimeFailure(f.ctx, f.project, integration.ID, integration.SetupRevision)
				if errors.Is(err, storeerr.ErrNotFound) {
					return nil
				}
				require.NoError(t, err)
				return &failure
			}
			require.Nil(t, read())
			revision := integrationstore.IntegrationRuntimeRevision{
				ProjectID: f.project, IntegrationID: integration.ID,
				SetupRevision: integration.SetupRevision, CredentialVersionID: versionID,
			}
			claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.True(t, found)
			require.Nil(t, read(), "a lease does not report a provider connection state")
			started := time.Now()
			require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claim.Lease, time.Hour, "Discord Gateway closed: 4014"))
			failure := read()
			require.NotNil(t, failure)
			require.Equal(t, "Discord Gateway closed: 4014", failure.Message)
			require.WithinDuration(t, started.Add(time.Hour), failure.RetryAt, 5*time.Second)
			wrongProject, err := f.store.GetIntegrationRuntimeFailure(
				f.ctx,
				uuid.New(),
				integration.ID,
				integration.SetupRevision,
			)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			require.Zero(t, wrongProject)
			switch scenario {
			case "retry_due", "reclaimed":
				f.exec(
					t,
					`UPDATE integration_runtime SET next_attempt_at=now()-interval '1 second' WHERE integration_id=$1`,
					integration.ID,
				)
				if scenario == "reclaimed" {
					_, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
					require.NoError(t, err)
					require.True(t, found)
				}
			case "stale_response", "setup_changed":
				f.exec(t, `UPDATE integrations SET setup_revision=setup_revision+1 WHERE id=$1`, integration.ID)
				if scenario == "setup_changed" {
					integration.SetupRevision++
				}
			case "rotated":
				_, _, err := secretStore.CreateSecretVersion(f.ctx, secretstore.CreateSecretVersionInput{
					OrgID: f.org, SecretID: integration.CredentialSecretID,
					Actor: identitystore.NewUserPrincipal(f.user), Material: secrets.GenericMaterial{Value: "rotated"},
				})
				require.NoError(t, err)
			case "secret_deleted":
				f.exec(t, `UPDATE secrets SET deleted_at=now() WHERE id=$1`, integration.CredentialSecretID)
			case "credential_unavailable":
				f.exec(t, `DELETE FROM secret_grants WHERE secret_id=$1 AND target_project_id=$2`,
					integration.CredentialSecretID, f.project)
			case "disconnected":
				_, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
					ProjectID: f.project, IntegrationID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
				})
				require.NoError(t, err)
			case "integration_deleted":
				f.exec(t, `UPDATE integrations SET deleted_at=now() WHERE id=$1`, integration.ID)
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

func TestIntegrationRuntimeFencesOwnershipAndCommitsReceiptWithCheckpoint(t *testing.T) {
	for _, scenario := range []string{"commit", "rollback", "expired", "rotation", "disable", "release"} {
		t.Run(scenario, func(t *testing.T) {
			f, secretStore, integration, versionID := newIntegrationRuntimeFixture(t)
			revision := integrationstore.IntegrationRuntimeRevision{
				ProjectID:           f.project,
				IntegrationID:       integration.ID,
				SetupRevision:       integration.SetupRevision,
				CredentialVersionID: versionID,
			}
			claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.True(t, found)
			require.Empty(t, claim.Checkpoint)
			_, found, err = f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
			require.NoError(t, err)
			require.False(t, found)
			if scenario == "commit" {
				owner := integrationdb.BeginTx(t, f.ctx, f.pool)
				_, err := owner.Exec(
					f.ctx,
					`SELECT integration_id FROM integration_runtime WHERE integration_id=$1 FOR UPDATE`,
					integration.ID,
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
				ProjectID:     f.project,
				IntegrationID: integration.ID,
				ReceiptKey:    "discord:789",
				Payload:       []byte(`{"type":"MESSAGE_CREATE"}`),
			}
			checkpoint := json.RawMessage(`{"session_id":"session","sequence":7}`)
			switch scenario {
			case "rollback":
				f.exec(t, `CREATE FUNCTION reject_runtime_checkpoint() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
      IF NEW.checkpoint IS NOT NULL THEN RAISE EXCEPTION 'injected checkpoint failure'; END IF; RETURN NEW; END $$;
      CREATE TRIGGER reject_runtime_checkpoint BEFORE UPDATE ON integration_runtime
      FOR EACH ROW EXECUTE FUNCTION reject_runtime_checkpoint()`)
			case "expired":
				f.exec(
					t,
					`UPDATE integration_runtime SET claim_expires_at=now()-interval '1 second' WHERE integration_id=$1`,
					integration.ID,
				)
			case "rotation":
				_, _, err := secretStore.CreateSecretVersion(
					f.ctx,
					secretstore.CreateSecretVersionInput{
						OrgID:    f.org,
						SecretID: integration.CredentialSecretID,
						Actor:    identitystore.NewUserPrincipal(f.user),
						Material: secrets.GenericMaterial{Value: "rotated"},
					},
				)
				require.NoError(t, err)
			case "disable":
				_, err := f.store.DisconnectIntegration(f.ctx, integrationstore.DisconnectIntegrationInput{
					ProjectID: f.project, IntegrationID: integration.ID, ExpectedSetupRevision: &integration.SetupRevision,
				})
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
				require.NoError(
					t,
					f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, json.RawMessage(`{"sequence":8}`), &receipt),
				)
				var count int
				require.NoError(
					t,
					f.pool.QueryRow(
						f.ctx,
						`SELECT count(*) FROM integration_inbox WHERE integration_id=$1`,
						integration.ID,
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
				f.pool.QueryRow(f.ctx, `SELECT count(*) FROM integration_inbox WHERE integration_id=$1`, integration.ID).
					Scan(&count),
			)
			require.Zero(t, count)
		})
	}
}

func TestIntegrationRuntimeRechecksExpiryAfterWaiting(t *testing.T) {
	f, _, integration, versionID := newIntegrationRuntimeFixture(t)
	revision := integrationstore.IntegrationRuntimeRevision{
		ProjectID:           f.project,
		IntegrationID:       integration.ID,
		SetupRevision:       integration.SetupRevision,
		CredentialVersionID: versionID,
	}
	claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	tx := integrationdb.BeginTx(t, f.ctx, f.pool)
	_, err = tx.Exec(
		f.ctx,
		`UPDATE integration_runtime SET claim_expires_at=now()-interval '1 second' WHERE integration_id=$1`,
		integration.ID,
	)
	require.NoError(t, err)
	done := integrationdb.RunAsyncError(
		func() error { return f.store.RenewIntegrationRuntime(f.ctx, claim.Lease, 30*time.Second) },
	)
	integrationdb.WaitForNamedLockWaiters(t, f.ctx, f.pool, "LockIntegrationRuntime", 1)
	require.NoError(t, tx.Commit(f.ctx))
	require.ErrorIs(
		t,
		integrationdb.Await(t, done, "renew expired runtime"),
		integrationstore.ErrIntegrationRuntimeLeaseLost,
	)
	wrong := claim.Lease
	wrong.IntegrationID = uuid.New()
	require.Error(t, f.store.CommitIntegrationRuntime(f.ctx, wrong, nil, nil))
}

func TestIntegrationRuntimeSettingsPreserveLeaseCheckpointAndBackoff(t *testing.T) {
	f, _, integration, versionID := newIntegrationRuntimeFixture(t)
	revision := integrationstore.IntegrationRuntimeRevision{
		ProjectID: f.project, IntegrationID: integration.ID,
		SetupRevision: integration.SetupRevision, CredentialVersionID: versionID,
	}
	claim, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	checkpoint := json.RawMessage(`{"session_id":"resume","sequence":9}`)
	require.NoError(t, f.store.CommitIntegrationRuntime(f.ctx, claim.Lease, checkpoint, nil))
	var profileID uuid.UUID
	require.NoError(
		t,
		f.pool.QueryRow(f.ctx, `SELECT id FROM agent_profiles WHERE project_id=$1`, f.project).Scan(&profileID),
	)
	updated, err := f.store.UpdateIntegration(f.ctx, integration.ID, integrationstore.SaveIntegrationInput{
		OrgID: f.org, ProjectID: f.project, Name: integration.Name, IntegrationKind: integration.IntegrationKind,
		Settings: integrationtest.ChatSettings("", profileID),
	})
	require.NoError(t, err)
	require.Equal(t, integration.SetupRevision, updated.SetupRevision)
	require.True(t, updated.UpdatedAt.After(integration.UpdatedAt))
	require.NoError(t, f.store.RenewIntegrationRuntime(f.ctx, claim.Lease, 30*time.Second))
	require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, claim.Lease, time.Hour, "retry later"))
	_, found, err = f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.False(t, found, "settings edits must not bypass transport backoff")
	f.exec(t, `UPDATE integration_runtime SET next_attempt_at=now() WHERE integration_id=$1`, integration.ID)
	resumed, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found)
	require.JSONEq(t, string(checkpoint), string(resumed.Checkpoint))
	require.NoError(t, f.store.ReleaseIntegrationRuntime(f.ctx, resumed.Lease, time.Hour, "retry later"))
	f.exec(t, `UPDATE integrations SET setup_revision=setup_revision+1 WHERE id=$1`, integration.ID)
	revision.SetupRevision++
	replaced, found, err := f.store.ClaimIntegrationRuntime(f.ctx, revision, 30*time.Second)
	require.NoError(t, err)
	require.True(t, found, "a changed setup can bypass backoff")
	require.Empty(t, replaced.Checkpoint, "old setup checkpoints must not be reused")
}
