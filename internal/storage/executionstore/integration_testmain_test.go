//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/internal/tokenutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/omnara-ai/omnara/internal/testutil/storagetest"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type ID = storage.ID
type Option = storage.Option
type ToolCallRecord = executionstore.ToolCallRecord
type CreateVerifiedUserInput = storagetest.CreateVerifiedUserInput

var NilID = storage.NilID

var WithBlobStore = storage.WithBlobStore
var WithMachinePoolProviders = storage.WithMachinePoolProviders
var WithModelCallRetryBackoff = storage.WithModelCallRetryBackoff
var WithPostCommitPublisher = storage.WithPostCommitPublisher
var WithSecretKeyWrapper = storage.WithSecretKeyWrapper

type Store struct {
	*storage.Store
	pool *pgxpool.Pool
	q    *dbsqlc.Queries
}

type IdentityStore struct {
	*identitystore.Store
	pool *pgxpool.Pool
}

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}

func newIntegrationKeyWrapper() secrets.KeyWrapper {
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"storage-integration-key",
		map[string][]byte{
			"storage-integration-key": []byte("0123456789abcdef0123456789abcdef"),
		},
	)
	if err != nil {
		panic(err)
	}
	return keyWrapper
}

func newIntegrationStore(pool *pgxpool.Pool, opts ...Option) *Store {
	allOpts := make([]Option, 0, len(opts)+1)
	allOpts = append(allOpts, WithSecretKeyWrapper(newIntegrationKeyWrapper()))
	allOpts = append(allOpts, opts...)
	return NewStore(pool, allOpts...)
}

func NewStore(pool *pgxpool.Pool, opts ...Option) *Store {
	return &Store{
		Store: storage.NewStore(pool, opts...),
		pool:  pool,
		q:     dbsqlc.New(pool),
	}
}

func (s *Store) Identity() *IdentityStore {
	return &IdentityStore{Store: s.Store.Identity(), pool: s.pool}
}

func (s *IdentityStore) CreateVerifiedUser(
	ctx context.Context,
	input CreateVerifiedUserInput,
) (identitystore.UserRecord, error) {
	return storagetest.CreateVerifiedUser(ctx, s.pool, input)
}

func ParseID(value string) (ID, error) {
	return storage.ParseID(value)
}

func newSecretIntegrationKeyWrapper() secrets.KeyWrapper {
	keyWrapper, err := secrets.NewLocalKeyWrapper(
		"test-key",
		map[string][]byte{"test-key": []byte("0123456789abcdef0123456789abcdef")},
	)
	if err != nil {
		panic(err)
	}
	return keyWrapper
}

func newSecretIntegrationStore(pool *pgxpool.Pool, opts ...Option) *Store {
	allOpts := []Option{
		WithSecretKeyWrapper(newSecretIntegrationKeyWrapper()),
		WithMachinePoolProviders(mergingMachinePoolProviders{}),
	}
	allOpts = append(allOpts, opts...)
	return newIntegrationStore(pool, allOpts...)
}

func createSecretTestUser(
	t *testing.T,
	ctx context.Context,
	store *Store,
	name, orgRole string,
) identitystore.UserRecord {
	t.Helper()
	user, err := store.Identity().CreateUser(
		ctx,
		identitystore.CreateUserInput{DisplayName: name},
	)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if _, err := store.Identity().AddOrgMembership(
		ctx,
		identitystore.AddOrgMembershipInput{
			OrgID:  testOrgID,
			UserID: user.ID,
			Role:   orgRole,
		},
	); err != nil {
		t.Fatalf("add org membership: %v", err)
	}
	return user
}

func waitForDatabaseTime(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	target time.Time,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `SELECT pg_sleep_until($1)`, target); err != nil {
		t.Fatalf("wait for database time: %v", err)
	}
}

func userPrincipal(id ID) identitystore.PrincipalRecord {
	return identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeUser, ID: id}
}

func isForeignKeyViolation(err error) bool {
	return storeutil.IsForeignKeyViolation(err)
}

func normalizedJSON(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`{}`)
	}
	return value
}

func normalizedJSONArray(value json.RawMessage) json.RawMessage {
	if len(value) == 0 {
		return json.RawMessage(`[]`)
	}
	return value
}

func normalizedJSONObject(value json.RawMessage, fieldName string) (json.RawMessage, error) {
	value = normalizedJSON(value)
	var object map[string]json.RawMessage
	if err := json.Unmarshal(value, &object); err != nil {
		return nil, fmt.Errorf("parse %s: %w", fieldName, err)
	}
	if object == nil {
		return nil, fmt.Errorf("%s must be a JSON object", fieldName)
	}
	return value, nil
}

func marshalJSON(value any) (json.RawMessage, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func sameJSON(a, b json.RawMessage) bool {
	return jsoncanonical.Equal(a, b)
}

func assertJSONRawEqual(t *testing.T, got json.RawMessage, want string) {
	t.Helper()
	if !sameJSON(got, json.RawMessage(want)) {
		t.Fatalf("json = %s, want %s", got, want)
	}
}

func intPtr(value int) *int {
	return &value
}

type mergingMachinePoolProviders struct{}

func (mergingMachinePoolProviders) ResolveMachineProviderOptions(
	_ string,
	defaultOptions map[string]json.RawMessage,
	projectOptions map[string]json.RawMessage,
	agentOptions map[string]json.RawMessage,
) (map[string]json.RawMessage, error) {
	var merged map[string]json.RawMessage
	for _, overlay := range []map[string]json.RawMessage{
		defaultOptions,
		projectOptions,
		agentOptions,
	} {
		if overlay != nil && merged == nil {
			merged = map[string]json.RawMessage{}
		}
		for key, value := range overlay {
			merged[key] = append(json.RawMessage(nil), value...)
		}
	}
	return merged, nil
}

func (mergingMachinePoolProviders) ValidatePool(
	_ string,
	_ executionstore.MachinePoolProviderPolicy,
) error {
	return nil
}

func (providers mergingMachinePoolProviders) BuildMachineProvisioningIntent(
	provider string,
	policy executionstore.MachinePoolProviderPolicy,
	machineProvisioning executionstore.MachineProvisioningConfig,
) (executionstore.MachineProvisioningConfig, error) {
	if err := providers.ValidatePool(provider, policy); err != nil {
		return executionstore.MachineProvisioningConfig{}, err
	}
	return machineProvisioning, nil
}

func sqlcTextFromEmpty(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

func sqlcIDFromNil(value ID) *ID {
	if value == NilID {
		return nil
	}
	return &value
}

func idFromSQLCPtrForTest(value *ID) ID {
	if value == nil {
		return NilID
	}
	return *value
}

func sqlcInt32Ptr(value *int) *int32 {
	if value == nil {
		return nil
	}
	converted := int32(*value)
	return &converted
}

func sameIntPtr(left, right *int) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}

func testMachineProvisioning(
	t *testing.T,
	cpu int,
	memoryMB int,
	providerOptions map[string]any,
) executionstore.MachineProvisioningConfig {
	t.Helper()
	rawProviderOptions := make(map[string]json.RawMessage, len(providerOptions))
	for key, value := range providerOptions {
		rawProviderOptions[key] = mustTestRawJSON(t, value)
	}
	return executionstore.MachineProvisioningConfig{
		CPU:             &cpu,
		MemoryMB:        &memoryMB,
		ProviderOptions: rawProviderOptions,
	}
}

func requireMachineProvisioningForTest(
	t *testing.T,
	got executionstore.MachineProvisioningConfig,
	want executionstore.MachineProvisioningConfig,
) {
	t.Helper()
	if !sameIntPtr(got.CPU, want.CPU) {
		t.Fatalf("machine provisioning cpu = %v, want %v", got.CPU, want.CPU)
	}
	if !sameIntPtr(got.MemoryMB, want.MemoryMB) {
		t.Fatalf("machine provisioning memory_mb = %v, want %v", got.MemoryMB, want.MemoryMB)
	}
	if len(got.ProviderOptions) != len(want.ProviderOptions) {
		t.Fatalf("machine provisioning provider_options = %+v, want %+v", got.ProviderOptions, want.ProviderOptions)
	}
	for key, wantValue := range want.ProviderOptions {
		if !sameJSON(got.ProviderOptions[key], wantValue) {
			t.Fatalf(
				"machine provisioning provider_options[%s] = %s, want %s",
				key,
				got.ProviderOptions[key],
				wantValue,
			)
		}
	}
}

func requireMachineEnvironmentForTest(
	t *testing.T,
	got, want executionstore.MachineEnvironment,
) {
	t.Helper()
	if !maps.Equal(got.Env, want.Env) {
		t.Fatalf("machine environment env = %+v, want %+v", got.Env, want.Env)
	}
	if !maps.Equal(got.SecretEnv, want.SecretEnv) {
		t.Fatalf("machine environment secret_env = %+v, want %+v", got.SecretEnv, want.SecretEnv)
	}
}

func mustTestRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return raw
}

func provisionDefaultMachinePoolGrantsForProject(
	ctx context.Context,
	store *Store,
	orgID, projectID ID,
) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := store.Execution().ProvisionOrganizationDefaultsTx(ctx, tx, orgID, projectID, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func setManagedWorkAdmissionForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	orgID ID,
	allowed bool,
) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
INSERT INTO org_managed_work_admission(org_id, new_managed_work_allowed)
VALUES ($1, $2)
ON CONFLICT (org_id) DO UPDATE
SET new_managed_work_allowed = EXCLUDED.new_managed_work_allowed
`, orgID, allowed); err != nil {
		t.Fatalf("set managed work admission: %v", err)
	}
}

func permissionRequestForStorageTest(t *testing.T, toolName string) json.RawMessage {
	t.Helper()
	authorization, err := toolpermission.NewAuthorization(
		toolName,
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	mode, ok := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		toolpermission.ModeAlwaysAsk,
	)
	if !ok {
		t.Fatal("always_ask mode missing")
	}
	value, err := toolpermission.NewAllowDenyForm("Permission requested", nil)
	if err != nil {
		t.Fatalf("interaction form: %v", err)
	}
	request, err := toolpermission.NewRequest(
		mode,
		toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		authorization,
		value,
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return raw
}

func randomTokenPart(size int) (string, error) {
	return tokenutil.RandomHex(size)
}

func newSecretUUID() (ID, error) {
	return uuid.NewV7()
}

func artifactObjectKey(agentID, artifactID ID) string {
	return "artifacts/" + agentID.String() + "/" + artifactID.String()
}

func publicResourceID(kind publicid.Kind, id ID) string {
	encoded, err := publicid.Encode(kind, id)
	if err != nil {
		return ""
	}
	return encoded
}
