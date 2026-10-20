package migrations

import (
	"testing"
)

func TestStorageToolsMigrationRenamesConfigTools(t *testing.T) {
	source := "name: agent\ntools:\n  read_file: {enabled: false}\n  upload_file: {}\n  run_command: {}\n"
	definition := []byte(`{"name":"agent","tools":{"read_file":{"enabled":false},"run_command":{},"upload_file":{}}}`)
	canonical, err := canonicalJSON(definition)
	if err != nil {
		t.Fatal(err)
	}
	config := storedAgentConfig{
		id: "config", source: source, sourceFormat: "yaml", sourceHash: hashBytes([]byte(source)),
		definition: definition, compiledDefinition: definition, effectiveDefinitionHash: hashBytes(canonical),
	}
	migrated, changed, err := migrateStorageToolConfig(config)
	if err != nil || !changed {
		t.Fatalf("changed=%t err=%v", changed, err)
	}
	wantSource := "name: agent\ntools:\n  storage_read: {enabled: false}\n  storage_upload: {}\n  run_command: {}\n"
	wantDefinition := `{"name":"agent","tools":{"run_command":{},"storage_read":{"enabled":false},"storage_upload":{}}}`
	if migrated.source != wantSource || string(migrated.definition) != wantDefinition ||
		string(migrated.compiledDefinition) != wantDefinition {
		t.Fatalf("migrated = %+v", migrated)
	}
	if migrated.sourceHash != hashBytes([]byte(wantSource)) ||
		migrated.effectiveDefinitionHash != hashBytes([]byte(wantDefinition)) {
		t.Fatalf("hashes = %s %s", migrated.sourceHash, migrated.effectiveDefinitionHash)
	}
	if _, changed, err := migrateStorageToolConfig(migrated); err != nil || changed {
		t.Fatalf("second run changed=%t err=%v", changed, err)
	}
}
