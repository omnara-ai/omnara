//go:build integration

package executionstore_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
)

const (
	testChannelConnector = storagetest.ChannelConnector
	testChannelProvider  = storagetest.ChannelProvider
	testChannelHandler   = storagetest.ChannelHandler
)

func createIntegrationProjectAdmin(
	t *testing.T,
	ctx context.Context,
	store *Store,
	email string,
) identitystore.UserRecord {
	t.Helper()
	return storagetest.CreateIntegrationProjectAdmin(t, ctx, store.Store, store.pool, testOrgID, testProjectID, email)
}

func createIntegrationTestProfile(
	t *testing.T,
	ctx context.Context,
	store *Store,
	key string,
) executionstore.AgentProfileRecord {
	t.Helper()
	return storagetest.CreateIntegrationTestProfile(t, ctx, store.Store, testOrgID, testProjectID, key)
}

func createIntegrationBoundAgent(
	t *testing.T,
	ctx context.Context,
	store *Store,
	profile executionstore.AgentProfileRecord,
	userID uuid.UUID,
	key string,
) executionstore.AgentRecord {
	t.Helper()
	return storagetest.CreateIntegrationBoundAgent(t, ctx, store.Store, testProjectID, profile, userID, key)
}

func createIntegrationCredential(
	t *testing.T,
	ctx context.Context,
	store *Store,
	projectID, createdByUserID uuid.UUID,
	label string,
) uuid.UUID {
	t.Helper()
	return storagetest.CreateIntegrationCredential(t, ctx, store.Store, testOrgID, projectID, createdByUserID, label)
}

func slackIntegrationInstallInput(
	appID, routeProfileID, installedByUserID, credentialSecretID uuid.UUID,
	providerAccountRef, providerTenantID string,
) integrationstore.UpsertIntegrationInstallInput {
	return storagetest.SlackIntegrationInstallInput(
		testOrgID, testProjectID, appID, routeProfileID, installedByUserID,
		credentialSecretID, providerAccountRef, providerTenantID,
	)
}

func createSlackIntegrationApp(
	t *testing.T, ctx context.Context, store *Store, projectID uuid.UUID, appRef string,
) uuid.UUID {
	t.Helper()
	return storagetest.CreateSlackIntegrationApp(t, ctx, store.Store, testOrgID, projectID, appRef)
}

func externalConnectionInput(userID uuid.UUID) integrationstore.CreateExternalIntegrationInstallInput {
	return storagetest.ExternalConnectionInput(testOrgID, testProjectID, userID)
}

func externalDefinitionInput(installID uuid.UUID) integrationstore.PublishChannelDefinitionInput {
	return storagetest.ExternalDefinitionInput(testProjectID, installID)
}

func createChannelLifecycleFixture(
	t *testing.T,
	ctx context.Context,
	store *Store,
	suffix string,
) (
	identitystore.UserRecord,
	executionstore.AgentRecord,
	integrationstore.IntegrationAppRecord,
	integrationstore.IntegrationInstallRecord,
) {
	t.Helper()
	return storagetest.CreateChannelLifecycleFixture(t, ctx, store.Store, store.pool, testOrgID, testProjectID, suffix)
}

func createChannelTestDefinition(
	t *testing.T, ctx context.Context, store *Store, install integrationstore.IntegrationInstallRecord,
) uuid.UUID {
	t.Helper()
	return storagetest.CreateChannelTestDefinition(t, ctx, store.Store, install)
}

func testChannelCapabilities(provider string) []channelconnector.Capability {
	return storagetest.ChannelCapabilities(provider)
}

func testChannelCapability(provider string) channelconnector.Capability {
	return storagetest.ChannelCapability(provider)
}

type channelAuthorityFixture struct {
	storagetest.ChannelAuthorityFixture
	Store *Store
}

func newChannelAuthorityFixture(t *testing.T, ctx context.Context, name string) channelAuthorityFixture {
	t.Helper()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newSecretIntegrationStore(pool)
	return channelAuthorityFixture{
		storagetest.NewChannelAuthorityFixture(t, ctx, store.Store, pool, testOrgID, testProjectID, name), store,
	}
}
