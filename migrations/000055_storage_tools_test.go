package migrations

import (
	"database/sql"
	"testing"
)

func TestStorageToolsMigrationRenamesConfigTools(t *testing.T) {
	source := "name: agent\ntools:\n  read_file: {enabled: false}\n  upload_file: {}\n  run_command: {}\n"
	// Compiled configs can include default tools, such as list_files, that the source omits.
	compiled := []byte(`{"name":"agent","tools":{"list_files":{},"read_file":{"enabled":false},` +
		`"run_command":{},"upload_file":{}}}`)
	hash, err := explicitDefaultToolsConfigHash(compiled)
	if err != nil {
		t.Fatal(err)
	}
	wantCompiled := `{"name":"agent","tools":{"run_command":{},"storage_list":{},` +
		`"storage_read":{"enabled":false},"storage_upload":{}}}`
	wantSource := "name: agent\ntools:\n  storage_read: {enabled: false}\n  storage_upload: {}\n  run_command: {}\n"
	for _, test := range []struct {
		name       string
		source     sql.NullString
		wantSource sql.NullString
	}{
		{"yaml source", sql.NullString{String: source, Valid: true}, sql.NullString{String: wantSource, Valid: true}},
		{"generated config", sql.NullString{}, sql.NullString{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := storageToolConfig{id: "config", compiled: compiled, hash: hash, source: test.source}
			if test.source.Valid {
				config.format = sql.NullString{String: "yaml", Valid: true}
				config.sourceHash = sql.NullString{String: hashBytes([]byte(source)), Valid: true}
			}
			migrated, changed, err := migrateStorageToolConfig(config)
			if err != nil || !changed {
				t.Fatalf("changed=%t err=%v", changed, err)
			}
			wantHash, err := explicitDefaultToolsConfigHash([]byte(wantCompiled))
			if err != nil {
				t.Fatal(err)
			}
			if string(migrated.compiled) != wantCompiled || migrated.hash != wantHash ||
				migrated.source != test.wantSource {
				t.Fatalf("migrated = %+v", migrated)
			}
			if test.source.Valid && migrated.sourceHash.String != hashBytes([]byte(wantSource)) {
				t.Fatalf("source hash = %s", migrated.sourceHash.String)
			}
			if _, changed, err := migrateStorageToolConfig(migrated); err != nil || changed {
				t.Fatalf("second run changed=%t err=%v", changed, err)
			}
		})
	}
}

func TestStorageToolsMigrationKeepsYAMLFormatting(t *testing.T) {
	for _, test := range []struct{ name, source, want string }{
		{
			"folded block",
			"instruction: >\n  Intro.\n\n   - indented line\ntools:\n  read_file:\n    enabled: false\n",
			"instruction: >\n  Intro.\n\n   - indented line\ntools:\n  storage_read:\n    enabled: false\n",
		},
		{"flow mapping", "tools: {list_files: {}, run_command: {}}\n", "tools: {storage_list: {}, run_command: {}}\n"},
		{"quoted key", "tools:\n  \"write_file\": {}\n  'search_files': {}\n",
			"tools:\n  \"storage_write\": {}\n  'storage_search': {}\n"},
		{"no tools", "instruction: hi\n", "instruction: hi\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, _, err := renameStorageToolsYAML([]byte(test.source))
			if err != nil || string(got) != test.want {
				t.Fatalf("renamed = %q, %v; want %q", got, err, test.want)
			}
		})
	}
	if _, _, err := renameStorageToolsYAML([]byte("tools:\n  read_file: {}\n  storage_read: {}\n")); err == nil {
		t.Fatal("accepted both old and new tool names")
	}
}
