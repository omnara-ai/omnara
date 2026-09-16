//go:build integration

package integrationstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
)

type Store struct {
	*storage.Store
	pool *pgxpool.Pool
	q    *dbsqlc.Queries
}

func TestMain(m *testing.M) { integrationdb.RunTestMain(m) }

func testID(seed string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("omnara-storage-integration:"+seed))
}

var (
	testOrgID     = testID("org_test")
	testProjectID = testID("project_test")
)

func openIntegrationDB(t *testing.T, ctx context.Context) *pgxpool.Pool {
	t.Helper()
	return integrationdb.OpenMigratedPool(t, ctx, "../../../migrations")
}

func seedMigratedDB(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	storagefixture.SeedProject(t, ctx, pool, storagefixture.ProjectIDs{
		OrgID: testOrgID, ProjectID: testProjectID,
		ProviderAdminUserID:     testID("default_provider_admin_user"),
		ProviderSecretID:        testID("default_provider_credential_secret"),
		ProviderSecretVersionID: testID("default_provider_credential_secret_version"),
		ProviderConfigID:        testID("default_provider_config"),
	}, time.Date(2026, 4, 27, 0, 0, 0, 0, time.UTC))
}

func newSecretIntegrationStore(pool *pgxpool.Pool, opts ...storage.Option) *Store {
	wrapper, err := secrets.NewLocalKeyWrapper(
		"test-key", map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		panic(err)
	}
	allOpts := append([]storage.Option{storage.WithSecretKeyWrapper(wrapper)}, opts...)
	return &Store{Store: storage.NewStore(pool, allOpts...), pool: pool, q: dbsqlc.New(pool)}
}

func isPgCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}

func sameJSON(a, b json.RawMessage) bool { return jsoncanonical.Equal(a, b) }
