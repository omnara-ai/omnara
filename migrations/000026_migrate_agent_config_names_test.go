package migrations

import (
	"encoding/json"
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
	raw := []byte("instruction: Test\nname: Legacy Agent\nmodel:\n  provider_config: Provider\n  name: Model\n")
	want := "instruction: Test\nmodel:\n  provider_config: Provider\n  name: Model\n"
	got, changed, err := migrateAgentConfigSource("yaml", raw)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || string(got) != want {
		t.Fatalf("migrated source = %q, changed=%t, want %q", got, changed, want)
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
