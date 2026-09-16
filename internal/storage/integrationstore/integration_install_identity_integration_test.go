//go:build integration

package integrationstore_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestManagedInstallationIdentityGateSerializesOnlyExactTuple(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, app, _ := createChannelInstallationFixture(t, ctx, store, "install-identity-lock")
	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	key := dbsqlc.LockIntegrationInstallIdentityParams{IntegrationAppID: app.ID, ProviderAccountRef: "account"}
	require.NoError(t, dbsqlc.New(holder).LockIntegrationInstallIdentity(ctx, key))
	var holderPID int32
	require.NoError(t, holder.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&holderPID))
	waitCtx, cancelWait := context.WithTimeout(ctx, 5*time.Second)
	defer cancelWait()
	waiter, err := pool.Begin(waitCtx)
	require.NoError(t, err)
	defer func() { _ = waiter.Rollback(ctx) }()
	done := make(chan error, 1)
	go func() { done <- dbsqlc.New(waiter).LockIntegrationInstallIdentity(waitCtx, key) }()
	integrationdb.WaitForLockWaitBlockedBy(t, waitCtx, pool, "-- name: LockIntegrationInstallIdentity ", holderPID)

	independentCtx, cancelIndependent := context.WithTimeout(ctx, 2*time.Second)
	defer cancelIndependent()
	independent, err := pool.Begin(independentCtx)
	require.NoError(t, err)
	defer func() { _ = independent.Rollback(ctx) }()
	nullTenant := "null"
	otherKey := key
	otherKey.ProviderTenantID = &nullTenant
	require.NoError(t, dbsqlc.New(independent).LockIntegrationInstallIdentity(independentCtx, otherKey),
		"NULL and a literal tenant value identify different installations")
	otherKey = key
	otherKey.ProviderAccountRef = "other-account"
	require.NoError(t, dbsqlc.New(independent).LockIntegrationInstallIdentity(independentCtx, otherKey))
	require.NoError(t, independent.Rollback(ctx))
	require.NoError(t, holder.Rollback(ctx))
	require.NoError(t, <-done, "an identical creator proceeds only after the first transaction ends")
	require.NoError(t, waiter.Rollback(ctx))

	delimiterHolder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = delimiterHolder.Rollback(ctx) }()
	tenant := "team:part"
	key.ProviderTenantID = &tenant
	require.NoError(t, dbsqlc.New(delimiterHolder).LockIntegrationInstallIdentity(ctx, key))
	otherTenant := "team"
	otherKey = key
	otherKey.ProviderTenantID, otherKey.ProviderAccountRef = &otherTenant, "part:account"
	probeCtx, cancelProbe := context.WithTimeout(ctx, 2*time.Second)
	defer cancelProbe()
	probe, err := pool.Begin(probeCtx)
	require.NoError(t, err)
	defer func() { _ = probe.Rollback(ctx) }()
	require.NoError(t, dbsqlc.New(probe).LockIntegrationInstallIdentity(probeCtx, otherKey),
		"delimiter-bearing fields must not alias another tuple")
}

func TestManagedInstallationDiscoveryPrecedesAppAndInstallationRowLocks(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	_, app, install := createChannelInstallationFixture(t, ctx, store, "install-identity-discovery")
	holder, err := pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = holder.Rollback(ctx) }()
	_, err = holder.Exec(ctx, `SELECT id FROM integration_installs WHERE id = $1 FOR UPDATE`, install.ID)
	require.NoError(t, err)
	_, err = holder.Exec(ctx, `SELECT id FROM integration_apps WHERE id = $1 FOR UPDATE`, app.ID)
	require.NoError(t, err)
	lookupCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	key := dbsqlc.GetIntegrationInstallByAppProviderAccountParams{
		IntegrationAppID: app.ID, ProviderTenantID: &install.ProviderTenantID, ProviderAccountRef: install.ProviderAccountRef,
	}
	row, err := dbsqlc.New(pool).GetIntegrationInstallByAppProviderAccount(lookupCtx, key)
	require.NoError(t, err, "discovery must not wait for app or installation row locks")
	require.Equal(t, install.ID, row.ID)
	require.Equal(t, install.ProjectID, row.ProjectID, "caller checks existing project scope before mutation")
	key.ProviderAccountRef = "missing-account"
	_, err = dbsqlc.New(pool).GetIntegrationInstallByAppProviderAccount(ctx, key)
	require.ErrorIs(t, err, pgx.ErrNoRows)
}

func TestManagedInstallationPhysicalIdentityIsGlobalOnlyForSlack(t *testing.T) {
	t.Parallel()
	for _, provider := range []string{integrationstore.IntegrationProviderSlack, testChannelProvider} {
		t.Run(provider, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			pool := openIntegrationDB(t, ctx)
			seedMigratedDB(t, ctx, pool)
			store := newSecretIntegrationStore(pool)
			admin := createIntegrationProjectAdmin(t, ctx, store, "physical-identity@example.com")
			other, err := store.Identity().CreateProjectForPrincipal(ctx, identitystore.CreateProjectForPrincipalInput{
				OrgID: testOrgID, Creator: identitystore.NewUserPrincipal(admin.ID), Name: "Other installation project",
			})
			require.NoError(t, err)
			inputs := make([]integrationstore.UpsertIntegrationInstallInput, 2)
			for i, projectID := range []uuid.UUID{testProjectID, other.ID} {
				appInput := integrationstore.CreateIntegrationAppInput{
					OrgID: testOrgID, OwnerProjectID: projectID, Provider: provider, ProviderAppRef: "A_SHARED",
					ConnectorKey: testChannelConnector, State: integrationstore.IntegrationAppStateActive,
				}
				credentialID := uuid.Nil
				if provider == integrationstore.IntegrationProviderSlack {
					appInput.ConnectorKey = channelconnector.BuiltInConnectorKey
					appInput.InstallationCredentialKind = "slack_app_credentials"
					credentialID = createIntegrationCredential(t, ctx, store, projectID, admin.ID, "identity-race")
				}
				app, err := store.Integrations().CreateIntegrationApp(ctx, appInput)
				require.NoError(t, err)
				inputs[i] = integrationstore.UpsertIntegrationInstallInput{
					OrgID: testOrgID, ProjectID: projectID, IntegrationAppID: app.ID,
					InstalledBy: identitystore.NewUserPrincipal(admin.ID), Provider: provider,
					IntegrationKind: integrationstore.IntegrationKindManaged, ConnectionMode: "webhook",
					State: integrationstore.IntegrationInstallStateActive, CredentialSecretID: credentialID,
					ProviderTenantID: "T_SHARED", ProviderAccountRef: "A_SHARED", OAuthFlowID: uuid.Must(uuid.NewV7()),
				}
			}
			records := make([]integrationstore.IntegrationInstallRecord, 2)
			errs := make([]error, 2)
			start := make(chan struct{})
			var workers sync.WaitGroup
			for i := range inputs {
				workers.Go(func() {
					<-start
					records[i], errs[i] = store.Integrations().UpsertIntegrationInstall(ctx, inputs[i])
				})
			}
			close(start)
			workers.Wait()
			wins := 0
			for i, err := range errs {
				consumed, consumedErr := store.Integrations().IntegrationOAuthFlowConsumed(ctx, inputs[i].OAuthFlowID)
				require.NoError(t, consumedErr)
				if err != nil {
					require.Equal(t, integrationstore.IntegrationProviderSlack, provider)
					require.ErrorIs(t, err, storeerr.ErrConflict)
					require.False(t, consumed, "the losing cross-app insert must not consume OAuth")
					continue
				}
				wins++
				require.True(t, consumed)
				require.Equal(t, inputs[i].ProjectID, records[i].ProjectID)
				require.Equal(t, inputs[i].OAuthFlowID, records[i].LastOAuthFlowID)
				if provider == integrationstore.IntegrationProviderSlack {
					resolved, err := dbsqlc.New(pool).GetSlackIntegrationInstallByIdentity(ctx,
						dbsqlc.GetSlackIntegrationInstallByIdentityParams{
							ProviderTenantID: inputs[i].ProviderTenantID, ProviderAccountRef: inputs[i].ProviderAccountRef,
						})
					require.NoError(t, err)
					require.Equal(t, records[i].ID, resolved.ID, "the signed Slack edge has one unambiguous owner")
				}
			}
			want := 2
			if provider == integrationstore.IntegrationProviderSlack {
				want = 1
			}
			require.Equal(t, want, wins)
			var count int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM integration_installs
WHERE provider = $1 AND provider_tenant_id = 'T_SHARED' AND provider_account_ref = 'A_SHARED' AND deleted_at IS NULL`,
				provider).Scan(&count))
			require.Equal(t, want, count, "other providers retain independent app-scoped identities")
		})
	}
}
