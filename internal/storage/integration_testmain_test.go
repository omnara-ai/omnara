//go:build integration

package storage

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/internal/tokenutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

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

func sameJSON(a, b json.RawMessage) bool {
	return jsoncanonical.Equal(a, b)
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

func mustTestRawJSON(t *testing.T, value any) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
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
