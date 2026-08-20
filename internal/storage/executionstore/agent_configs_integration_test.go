//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCreateAgentConfigRejectsInvalidSource(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	for name, source := range map[string]string{
		"invalid resource reference": `
instruction: test
model:
  provider_config: " openai-prod"
  name: test
`,
		"legacy top-level name": `
name: legacy-agent
instruction: test
model:
  provider_config: openai-prod
  name: test
`,
		"empty":           "",
		"whitespace only": " \n\t",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
				ProjectID:    testProjectID,
				Source:       source,
				SourceFormat: "yaml",
			})
			if !errors.Is(err, storeerr.ErrInvalidRequest) {
				t.Fatalf("create agent config error = %v, want invalid request", err)
			}
		})
	}
}

func TestCreateAgentConfigRejectsUnresolvedModelContract(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	sourceYAML := testAgentConfigYAML()
	compiled := mustCompileAgentYAML(t, sourceYAML)
	configuredModel := ensureTestConfiguredModelForSource(t, ctx, store, sourceYAML, now)
	_, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
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
	now := time.Date(2026, 6, 10, 9, 30, 0, 0, time.UTC)
	sourceYAML := `
instruction: test
model:
  provider_config: openai-prod
  name: grant-required
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	configuredModelID := parseConfiguredModelID(t, compiled)
	grant, err := store.Models().GetActiveProjectModelGrantForConfiguredModel(ctx, testOrgID, testProjectID, configuredModelID)
	if err != nil {
		t.Fatalf("load active model grant: %v", err)
	}
	if _, err := store.Models().DeleteProjectModelGrant(ctx, testOrgID, testProjectID, grant.ID); err != nil {
		t.Fatalf("revoke model grant: %v", err)
	}
	_, err = store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModelID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
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
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC)
	sourceYAML := `
instruction: test
model:
  provider_config: openai-prod
  name: runtime-bounds
  default_max_output_tokens: 9000
`
	compiled := mustCompileAgentYAMLResolved(t, ctx, store, sourceYAML, now)
	_, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       parseConfiguredModelID(t, compiled),
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
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
		MaxOutputTokens:       8192,
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
		ResolveModelSelection: func(providerConfigName string, configuredModelName string) (agentconfig.ResolvedModelSelection, error) {
			return resolvedTestModelSelection(configuredModel), nil
		},
	})
	if err != nil {
		t.Fatalf("compile resolved agent yaml: %v", err)
	}
	if _, err := store.Execution().CreateAgentConfig(ctx, executionstore.CreateAgentConfigInput{
		ProjectID:               testProjectID,
		Definition:              json.RawMessage(compiled.CanonicalJSON),
		Source:                  sourceYAML,
		SourceFormat:            "yaml",
		ConfiguredModelID:       configuredModel.ID,
		CompiledDefinition:      json.RawMessage(compiled.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: compiled.Hash,
	}); err != nil {
		t.Fatalf("create agent config with project modality restrictions: %v", err)
	}
}
