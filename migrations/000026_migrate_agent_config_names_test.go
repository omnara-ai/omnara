package migrations

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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

func TestMigrateAgentConfigYAMLSourceRemovesOnlySimpleTopLevelName(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "first line",
			raw: "name: Legacy Agent\ninstruction: Test\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "middle line",
			raw: "instruction: Test\nname: Legacy Agent\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "quoted",
			raw: "name: \"Legacy Agent\"\ninstruction: Test\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n",
		},
		{
			name: "null machine sources",
			raw: "name: Legacy Agent\ninstruction: Test\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\nmachine_sources:\n",
			want: "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\nmachine_sources:\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, changed, err := migrateAgentConfigSource(
				agentConfigNameMigrationSourceFormatYAML,
				[]byte(test.raw),
			)
			if err != nil {
				t.Fatal(err)
			}
			if !changed || string(got) != test.want {
				t.Fatalf("migrated source = %q, changed=%t, want %q", got, changed, test.want)
			}
		})
	}
}

func TestMigrateAgentConfigYAMLSourceRejectsUnexpectedLegacyNameShape(t *testing.T) {
	for name, legacyName := range map[string]string{
		"multiline block":  "name: |\n  Legacy Agent",
		"multiline quoted": "name: \"Legacy\n  Agent\"",
	} {
		t.Run(name, func(t *testing.T) {
			raw := legacyName + "\ninstruction: Test\nmodel:\n" +
				"  provider_config: Provider\n  name: Model\n"
			_, _, err := migrateAgentConfigSource(
				agentConfigNameMigrationSourceFormatYAML,
				[]byte(raw),
			)
			if err == nil || !strings.Contains(err.Error(), "standalone scalar line") {
				t.Fatalf("legacy name shape error = %v", err)
			}
		})
	}
}

func TestMigrateAgentConfigJSONSourceRemovesOnlyTopLevelName(t *testing.T) {
	raw := []byte(`{"name":"Legacy","instruction":"Test","model":{"provider_config":"Provider","name":"Model"}}`)
	got, changed, err := migrateAgentConfigSource(agentConfigNameMigrationSourceFormatJSON, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed {
		t.Fatal("legacy source was not changed")
	}
	var parsed map[string]any
	if err := json.Unmarshal(got, &parsed); err != nil {
		t.Fatal(err)
	}
	if _, exists := parsed["name"]; exists {
		t.Fatal("migrated source retained top-level name")
	}
	model, ok := parsed["model"].(map[string]any)
	if !ok {
		t.Fatalf("model = %T, want object", parsed["model"])
	}
	if model["name"] != "Model" {
		t.Fatalf("model name = %v, want Model", model["name"])
	}
}

func TestMigrateAgentConfigSourceFailsClosedOnInvalidResourceReferences(t *testing.T) {
	tests := map[string]string{
		"boundary whitespace": " Provider ",
		"over limit":          strings.Repeat("x", agentConfigNameMigrationMaxCodePoints+1),
		"invisible":           "Build\u00a0Pool",
	}
	for name, provider := range tests {
		t.Run(name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{
				"name":        "Legacy",
				"instruction": "Test",
				"model": map[string]any{
					"provider_config": provider,
					"name":            "Model",
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = migrateAgentConfigSource(agentConfigNameMigrationSourceFormatJSON, raw)
			if err == nil || !strings.Contains(err.Error(), "model.provider_config") {
				t.Fatalf("resource reference error = %v", err)
			}
		})
	}
}

func TestMigrateAgentConfigYAMLSourcePreservesDecomposedResourceReferences(t *testing.T) {
	provider := strings.Repeat("e\u0301", agentConfigNameMigrationMaxCodePoints)
	raw := []byte("name: Legacy\ninstruction: Test\nmodel:\n  provider_config: " + provider + "\n  name: Model\n")
	want := raw[len("name: Legacy\n"):]
	got, changed, err := migrateAgentConfigSource(agentConfigNameMigrationSourceFormatYAML, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !bytes.Equal(got, want) {
		t.Fatalf("migrated source = %q, changed=%t, want %q", got, changed, want)
	}
}

func TestMigrateAgentConfigYAMLSourceDoesNotMutateAliases(t *testing.T) {
	raw := []byte("name: Legacy\ninstruction: &shared Café\nmodel:\n" +
		"  provider_config: *shared\n  name: Model\n")
	want := raw[len("name: Legacy\n"):]
	got, changed, err := migrateAgentConfigSource(agentConfigNameMigrationSourceFormatYAML, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || !bytes.Equal(got, want) {
		t.Fatalf("migrated source = %q, changed=%t, want %q", got, changed, want)
	}
}

func TestMigrateAgentConfigSourcePreservesCurrentInput(t *testing.T) {
	raw := []byte("instruction: Test\nmodel: {provider_config: Provider, name: Model}\n")
	got, changed, err := migrateAgentConfigSource(agentConfigNameMigrationSourceFormatYAML, raw)
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
	compiledCanonical, err := canonicalJSON(compiled)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := migrateStoredAgentConfig(storedAgentConfig{
		id:                      "00000000-0000-0000-0000-000000000001",
		projectID:               "00000000-0000-0000-0000-000000000002",
		source:                  source,
		sourceFormat:            "yaml",
		sourceHash:              hashBytes([]byte(source)),
		definition:              compiled,
		compiledDefinition:      compiled,
		effectiveDefinitionHash: hashBytes(compiledCanonical),
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

func TestMigrateStoredAgentConfigRejectsHashMismatch(t *testing.T) {
	valid := storedAgentConfig{
		source:                  "name: Legacy\ninstruction: Test\nmodel: {provider_config: Provider, name: Model}\n",
		sourceFormat:            "yaml",
		definition:              []byte(`{"name":"Legacy"}`),
		compiledDefinition:      []byte(`{"name":"Legacy"}`),
		effectiveDefinitionHash: hashBytes([]byte(`{"name":"Legacy"}`)),
	}
	valid.sourceHash = hashBytes([]byte(valid.source))

	staleSource := valid
	staleSource.sourceHash = "stale"
	if _, err := migrateStoredAgentConfig(staleSource); err == nil ||
		!strings.Contains(err.Error(), "source hash does not match source") {
		t.Fatalf("source hash error = %v", err)
	}

	staleDefinition := valid
	staleDefinition.effectiveDefinitionHash = "stale"
	if _, err := migrateStoredAgentConfig(staleDefinition); err == nil ||
		!strings.Contains(err.Error(), "effective definition hash") {
		t.Fatalf("definition hash error = %v", err)
	}
}

func TestMigrateStoredAgentConfigRejectsInconsistentLegacyMarkers(t *testing.T) {
	source := "instruction: Test\nmodel: {provider_config: Provider, name: Model}\n"
	compiled := []byte(`{"name":"Legacy"}`)
	_, err := migrateStoredAgentConfig(storedAgentConfig{
		source:                  source,
		sourceFormat:            "yaml",
		sourceHash:              hashBytes([]byte(source)),
		definition:              compiled,
		compiledDefinition:      compiled,
		effectiveDefinitionHash: hashBytes(compiled),
	})
	if err == nil || !strings.Contains(err.Error(), "markers are inconsistent") {
		t.Fatalf("legacy marker error = %v", err)
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
