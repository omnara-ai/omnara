//go:build integration

package executionstore_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestCreateAgentConfigWithoutSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	source := testAgentConfigYAML()
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, source)
	input := executionstore.CreateAgentConfigInput{
		ProjectID: testProjectID, ConfiguredModelID: parseConfiguredModelID(t, compiled),
		CompiledDefinition:      compiled.CanonicalJSON,
		EffectiveDefinitionHash: compiled.Hash,
	}
	first, err := store.Execution().CreateAgentConfig(ctx, input)
	require.NoError(t, err)
	second, err := store.Execution().CreateAgentConfig(ctx, input)
	require.NoError(t, err)
	require.Equal(t, first.ID, second.ID)
	require.False(t, second.Created)
	var noSource bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT source IS NULL AND source_format IS NULL AND source_hash IS NULL FROM agent_configs WHERE id = $1`,
		first.ID).Scan(&noSource))
	require.True(t, noSource)
	loaded, found, err := store.Execution().GetAgentConfig(ctx, testProjectID, first.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Empty(t, loaded.Source)
	require.JSONEq(t, string(compiled.CanonicalJSON), string(loaded.CompiledDefinition))
	input.SourceFormat = "yaml"
	_, err = store.Execution().CreateAgentConfig(ctx, input)
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	input.Source = source
	authored, err := store.Execution().CreateAgentConfig(ctx, input)
	require.NoError(t, err)
	require.NotEqual(t, first.ID, authored.ID)
	require.Equal(t, source, authored.Source)
}

// Versions have no unique key, so only the advisory lock keeps concurrent
// identical creates from inserting duplicates.
func TestCreateAgentConfigConcurrentIdenticalCreatesShareOneVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	source := "instruction: concurrent\nmodel:\n  provider_config: openai-prod\n  name: test\n"
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, source)
	base := executionstore.CreateAgentConfigInput{
		ProjectID: testProjectID, ConfiguredModelID: parseConfiguredModelID(t, compiled),
		CompiledDefinition:      compiled.CanonicalJSON,
		EffectiveDefinitionHash: compiled.Hash,
	}
	// Seed the definition so its unique key cannot serialize the creates below.
	seedInput := base
	seedInput.Source = "# seed\n" + source
	seed, err := store.Execution().CreateAgentConfig(ctx, seedInput)
	require.NoError(t, err)
	var definitionID uuid.UUID
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT definition_id FROM agent_configs WHERE id = $1`, seed.ID).Scan(&definitionID))

	withSource := base
	withSource.Source = source
	sourceHash := sha256.Sum256([]byte(source))
	for name, tc := range map[string]struct {
		input      executionstore.CreateAgentConfigInput
		sourceHash string
	}{
		"with source": {input: withSource, sourceHash: hex.EncodeToString(sourceHash[:])},
		"sourceless":  {input: base},
	} {
		control, err := pool.Begin(ctx)
		require.NoError(t, err)
		_, err = control.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended(
    'agent_config_version:' || $1::uuid::text || ':' || $2::uuid::text || ':' || $3::text, 0))`,
			testProjectID, definitionID, tc.sourceHash)
		require.NoError(t, err)

		const workers = 2
		records := make([]executionstore.AgentConfigRecord, workers)
		errs := make([]error, workers)
		var wg sync.WaitGroup
		for index := range workers {
			wg.Add(1)
			go func() {
				defer wg.Done()
				records[index], errs[index] = store.Execution().CreateAgentConfig(ctx, tc.input)
			}()
		}
		integrationdb.WaitForNamedLockWaiters(t, ctx, pool, "LockAgentConfigVersionKey", workers)
		require.NoError(t, control.Rollback(ctx))
		wg.Wait()

		created := 0
		for index := range workers {
			require.NoError(t, errs[index], name)
			require.Equal(t, records[0].ID, records[index].ID, "%s: identical creates must return one config", name)
			if records[index].Created {
				created++
			}
		}
		require.Equal(t, 1, created, "%s: exactly one create should insert", name)
		var versions int
		require.NoError(t, pool.QueryRow(ctx, `
SELECT count(*)
FROM agent_configs config
JOIN agent_configs created ON created.id = $1
WHERE config.project_id = created.project_id
  AND config.definition_id = created.definition_id
  AND config.source IS NOT DISTINCT FROM created.source`, records[0].ID).Scan(&versions))
		require.Equal(t, 1, versions, "%s: concurrent creates must not insert duplicate versions", name)
	}
}

func TestCreateAgentConfigMemoryStoreIDs(t *testing.T) {
	ctx := t.Context()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	admin := createSecretTestUser(t, ctx, store, "Memory Admin", "admin")
	scope := memorystore.Scope{OrgID: testOrgID, ProjectID: testProjectID, Principal: userPrincipal(admin.ID)}
	var attachments []agentconfig.MemoryStoreCompiled
	for _, name := range []string{"first", "second"} {
		memory, err := store.Memories().Create(ctx, scope, name, "", agentconfig.MemoryStoreAccessReadWrite)
		require.NoError(t, err)
		attachments = append(attachments, agentconfig.MemoryStoreCompiled{
			ID: memory.ID, Access: agentconfig.MemoryStoreAccessRead,
		})
	}
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, testAgentConfigYAML())
	var definition agentconfig.Compiled
	require.NoError(t, json.Unmarshal(compiled.CanonicalJSON, &definition))
	writable := attachments[0]
	writable.Access = agentconfig.MemoryStoreAccessReadWrite
	for _, test := range []struct {
		name        string
		attachments []agentconfig.MemoryStoreCompiled
		invalid     bool
	}{
		{name: "distinct", attachments: attachments},
		{
			name: "duplicate", invalid: true,
			attachments: []agentconfig.MemoryStoreCompiled{attachments[0], attachments[1], attachments[0]},
		},
		{
			name: "conflicting access", invalid: true,
			attachments: []agentconfig.MemoryStoreCompiled{attachments[0], attachments[1], writable},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			definition.MemoryStores = test.attachments
			encoded, err := agentconfig.EncodeCompiled(definition)
			require.NoError(t, err)
			_, err = store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
				ProjectID: testProjectID, ConfiguredModelID: parseConfiguredModelID(t, compiled),
				CompiledDefinition: encoded.CanonicalJSON, EffectiveDefinitionHash: encoded.Hash,
			})
			if test.invalid {
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
				require.ErrorContains(t, err, "duplicate memory store id")
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestCreateAgentConfigRejectsInvalidSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	_, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID: testProjectID,
		Source: `
instruction: test
model:
  provider_config: " openai-prod"
  name: test
`,
		SourceFormat: "yaml",
	})
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("create agent config error = %v, want invalid request", err)
	}
}

func TestCreateAgentConfigRejectsUnresolvedModelContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	sourceYAML := testAgentConfigYAML()
	compiled := mustCompileAgentYAML(t, sourceYAML)
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, sourceYAML)
	_, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		EffectiveDefinitionHash: compiled.Hash,
	})
	if err == nil {
		t.Fatal("expected unresolved compiled model contract to be rejected")
	}
}

func TestCreateAgentConfigRequiresActiveProjectModelGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	sourceYAML := `
instruction: test
model:
  provider_config: openai-prod
  name: grant-required
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML)
	configuredModelID := parseConfiguredModelID(t, compiled)
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(
		ctx, testOrgID, testProjectID, configuredModelID,
	)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("revoke model grant: %v", err)
	}
	_, err = store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModelID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		EffectiveDefinitionHash: compiled.Hash,
	})
	if !errors.Is(err, storeerr.ErrNotFound) {
		t.Fatalf("create agent config after model grant unavailable error = %v, want ErrNotFound", err)
	}
}

func TestCreateAgentConfigValidatesRuntimeModelOverridesAgainstGrant(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	sourceYAML := `
instruction: test
model:
  provider_config: openai-prod
  name: runtime-bounds
  default_max_output_tokens: 9000
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML)
	_, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		EffectiveDefinitionHash: compiled.Hash,
	})
	if !errors.Is(err, storeerr.ErrInvalidModelProviderConfig) {
		t.Fatalf("create agent config with model runtime options error = %v, want ErrInvalidModelProviderConfig", err)
	}
}

func TestCreateAgentConfigAllowsProjectModelGrantModalityRestrictions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	providerConfig, err := store.Models().GetModelProviderConfigByName(ctx, testOrgID, "openai-prod")
	if err != nil {
		t.Fatalf("load provider config: %v", err)
	}
	configuredModel, err := store.Models().CreateConfiguredModel(ctx, modelstore.CreateConfiguredModelInput{
		OrgID:                 testOrgID,
		ModelProviderConfigID: providerConfig.ID,
		Name:                  "image-only-runtime",
		ProviderModelSlug:     "gpt-image-runtime",
		ContextWindowTokens:   128000,
		MaxOutputTokens:       new(8192),
		InputModalities:       []string{"text", "image"},
		OutputModalities:      []string{"text"},
	})
	if err != nil {
		t.Fatalf("create configured model: %v", err)
	}
	if _, err := store.Models().CreateProjectModelGrant(ctx, modelstore.CreateProjectModelGrantInput{
		OrgID:             testOrgID,
		ProjectID:         testProjectID,
		ConfiguredModelID: configuredModel.ID,
		InputModalities:   []string{"image"},
		OutputModalities:  []string{"text"},
	}); err != nil {
		t.Fatalf("create project model grant: %v", err)
	}
	sourceYAML := `
instruction: test
model:
  provider_config: openai-prod
  name: image-only-runtime
`
	compiled, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(sourceYAML), agentconfig.CompileOptions{
		ResolveModelSelection: func(
			providerConfigName string,
			configuredModelName string,
		) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent yaml: %v", err)
	}
	if _, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		EffectiveDefinitionHash: compiled.Hash,
	}); err != nil {
		t.Fatalf("create agent config with project modality restrictions: %v", err)
	}
}
