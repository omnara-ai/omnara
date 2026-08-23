package migrations

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
)

func TestAgentConfigNameMigrationMetadata(t *testing.T) {
	migration := NewAgentConfigNameMigration()
	if migration.Version != AgentConfigNameMigrationVersion {
		t.Fatalf("migration version = %d, want %d", migration.Version, AgentConfigNameMigrationVersion)
	}
	if migration.Source != AgentConfigNameMigrationFile {
		t.Fatalf("migration source = %q, want %q", migration.Source, AgentConfigNameMigrationFile)
	}
}

func TestMigrateAgentConfigYAMLSourceRemovesOnlyTopLevelName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "first",
			raw:  "# agent config\nname: Legacy Agent\ninstruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
			want: "# agent config\ninstruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "comment after name",
			raw: "name: Legacy Agent\n# model settings\n\ninstruction: Test\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\n",
			want: "# model settings\n\ninstruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "multiline middle",
			raw:  "instruction: Test\nname: |\n  Legacy\n  Agent\nmodel:\n  provider_config: Provider\n  name: Model\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "multiline quoted comment",
			raw: "name: \"Legacy\n  # Agent\"\ninstruction: Test\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "last",
			raw:  "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\nname: Legacy Agent\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, changed, err := migrateAgentConfigYAMLSource([]byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("legacy source was not changed")
			}
			if string(got) != test.want {
				t.Fatalf("migrated source = %q, want %q", got, test.want)
			}
			if _, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, got); err != nil {
				t.Fatalf("parse migrated source: %v", err)
			}
		})
	}
}

func TestMigrateAgentConfigSourceRepairsResourceReferences(t *testing.T) {
	overlong := strings.Repeat("x", 65)
	tests := []struct {
		name   string
		format agentconfig.SourceFormat
		raw    string
	}{
		{
			name:   "yaml",
			format: agentconfig.SourceFormatYAML,
			raw: "instruction: Test\nmodel:\n" +
				"  provider_config: ' Provider '\n" +
				"  name: '" + overlong + "'\n" +
				"machine_sources:\n  - machine_pool_name: ' Pool '\n",
		},
		{
			name:   "json",
			format: agentconfig.SourceFormatJSON,
			raw: `{"name":"Legacy Agent","instruction":"Test","model":` +
				`{"provider_config":" Provider ","name":"` + overlong + `"},` +
				`"machine_sources":[{"machine_pool_name":" Pool "}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, changed, err := migrateAgentConfigSource(test.format, []byte(test.raw))
			if err != nil {
				t.Fatal(err)
			}
			if !changed {
				t.Fatal("invalid references were not changed")
			}
			parsed, err := agentconfig.ParseSource(test.format, got)
			if err != nil {
				t.Fatalf("parse migrated source: %v", err)
			}
			if parsed.Model.ProviderConfig != "Provider" ||
				parsed.Model.Name != strings.Repeat("x", 64) ||
				parsed.MachineSources[0].MachinePoolName != "Pool" {
				t.Fatalf("migrated references = %+v", parsed)
			}
		})
	}
}

func TestMigrateAgentConfigYAMLSourcePreservesUntouchedSemanticsDuringReencode(t *testing.T) {
	raw := []byte(`name: Legacy Agent
version: v1
instruction: |
  Preserve this block exactly as configuration data.
model:
  provider_config: " Provider "
  name: " Model "
  context_window_tokens: 32000
  default_max_output_tokens: 1024
  cache_retention: short
  reasoning:
    effort: high
machine_sources:
  - machine_pool_name: " Pool "
    max_machines: 3
    initial_num_machines: 1
    delete_after_idle_minutes: 15
    cwd: /workspace
    machine_cpu: 4
    machine_memory_mb: 8192
    env_overlay:
      MODE: test
      OPTIONAL:
    machine_provider_options_overlay:
      region: us-west-2
    description: Build machine
`)
	wantSource := []byte(`version: v1
instruction: |
  Preserve this block exactly as configuration data.
model:
  provider_config: Provider
  name: Model
  context_window_tokens: 32000
  default_max_output_tokens: 1024
  cache_retention: short
  reasoning:
    effort: high
machine_sources:
  - machine_pool_name: Pool
    max_machines: 3
    initial_num_machines: 1
    delete_after_idle_minutes: 15
    cwd: /workspace
    machine_cpu: 4
    machine_memory_mb: 8192
    env_overlay:
      MODE: test
      OPTIONAL:
    machine_provider_options_overlay:
      region: us-west-2
    description: Build machine
`)

	got, changed, err := migrateAgentConfigYAMLSource(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("legacy source was not changed")
	}
	gotParsed, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, got)
	if err != nil {
		t.Fatalf("parse migrated source: %v", err)
	}
	wantParsed, err := agentconfig.ParseSource(agentconfig.SourceFormatYAML, wantSource)
	if err != nil {
		t.Fatalf("parse expected source: %v", err)
	}
	if !reflect.DeepEqual(gotParsed, wantParsed) {
		t.Fatalf("migrated source changed untouched semantics\n got: %+v\nwant: %+v", gotParsed, wantParsed)
	}
}

func TestMigrateAgentConfigSourcePreservesCurrentInput(t *testing.T) {
	raw := []byte("instruction: Test\nmodel: {provider_config: Provider, name: Model}\n")
	got, changed, err := migrateAgentConfigSource(agentconfig.SourceFormatYAML, raw)
	if err != nil {
		t.Fatal(err)
	}
	if changed || !bytes.Equal(got, raw) {
		t.Fatalf("current source changed: changed=%t source=%q", changed, got)
	}
}

func TestMigrateStoredAgentConfigRemovesLegacyNameAndRehashes(t *testing.T) {
	source := "name: Legacy Agent\ninstruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n"
	compiled := []byte(`{"name":"Legacy Agent","instruction":"Test","model":{}}`)
	migrated, err := migrateStoredAgentConfig(storedAgentConfig{
		id:                      "00000000-0000-0000-0000-000000000001",
		projectID:               "00000000-0000-0000-0000-000000000002",
		source:                  source,
		sourceFormat:            "yaml",
		sourceHash:              hashBytes([]byte(source)),
		definition:              compiled,
		compiledDefinition:      compiled,
		compilerVersion:         agentconfig.CompilerVersion,
		effectiveDefinitionHash: "legacy-definition-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !migrated.changed {
		t.Fatal("legacy config was not changed")
	}
	if migrated.sourceHash != hashBytes([]byte(migrated.source)) {
		t.Fatal("migrated source hash does not match source")
	}
	if migrated.effectiveDefinitionHash != hashBytes(migrated.compiledDefinition) {
		t.Fatal("migrated definition hash does not match compiled definition")
	}
	for field, raw := range map[string][]byte{
		"definition":          migrated.definition,
		"compiled definition": migrated.compiledDefinition,
	} {
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			t.Fatalf("parse migrated %s: %v", field, err)
		}
		if _, ok := object["name"]; ok {
			t.Fatalf("migrated %s retained top-level name", field)
		}
	}
}

func TestMigrateStoredAgentConfigRejectsSourceHashMismatch(t *testing.T) {
	_, err := migrateStoredAgentConfig(storedAgentConfig{
		source:                  "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		sourceFormat:            "yaml",
		sourceHash:              "stale",
		definition:              []byte(`{}`),
		compiledDefinition:      []byte(`{}`),
		effectiveDefinitionHash: hashBytes([]byte(`{}`)),
	})
	if err == nil || !strings.Contains(err.Error(), "source hash does not match source") {
		t.Fatalf("source hash error = %v", err)
	}
}

func TestValidateMigratedAgentConfigKeysRejectsCollision(t *testing.T) {
	configs := []migratedAgentConfig{
		{storedAgentConfig: storedAgentConfig{
			id: "first", projectID: "project", effectiveDefinitionHash: "definition",
			sourceFormat: "yaml", sourceHash: "source",
		}},
		{storedAgentConfig: storedAgentConfig{
			id: "second", projectID: "project", effectiveDefinitionHash: "definition",
			sourceFormat: "yaml", sourceHash: "source",
		}},
	}
	err := validateMigratedAgentConfigKeys(configs)
	if err == nil || !strings.Contains(err.Error(), "first and second collide") {
		t.Fatalf("collision error = %v", err)
	}
}
