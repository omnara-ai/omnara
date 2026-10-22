package migrations

import (
	"database/sql"
	"strings"
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

func TestStorageToolsMigrationKeepsJSONFormatting(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
		changed            bool
	}{
		{
			name: "field order and whitespace",
			source: " {\r\n  \"tools\": { \"read_file\" : {}, \"write_file\": {} },\r\n" +
				"  \"instruction\": \"Use read_file.\"\r\n}\n",
			want: " {\r\n  \"tools\": { \"storage_read\" : {}, \"storage_write\": {} },\r\n" +
				"  \"instruction\": \"Use read_file.\"\r\n}\n",
			changed: true,
		},
		{
			name: "string escapes and nested keys",
			source: `{"instruction":"\u003c雪\u003e\nread_file\t\"tools\"",` +
				`"tools":{"read_file":{},"custom":{"input_schema":{"properties":{"read_file":{}}}}},` +
				`"other":{"tools":{"read_file":{}}},"number":9007199254740993,"exponent":1e+09}`,
			want: `{"instruction":"\u003c雪\u003e\nread_file\t\"tools\"",` +
				`"tools":{"storage_read":{},"custom":{"input_schema":{"properties":{"read_file":{}}}}},` +
				`"other":{"tools":{"read_file":{}}},"number":9007199254740993,"exponent":1e+09}`,
			changed: true,
		},
		{
			name:    "escaped tool key",
			source:  `{"\u0074ools":{"read_\u0066ile":{"enabled":false}}}`,
			want:    `{"\u0074ools":{"storage_read":{"enabled":false}}}`,
			changed: true,
		},
		{
			name: "all renames",
			source: `{"tools":{"download_file":{},"upload_file":{},"search_files":{},` +
				`"list_files":{},"write_file":{},"read_file":{}}}`,
			want: `{"tools":{"storage_download":{},"storage_upload":{},"storage_search":{},` +
				`"storage_list":{},"storage_write":{},"storage_read":{}}}`,
			changed: true,
		},
		{
			name:   "no source tools",
			source: "{ \"instruction\": \"read_file\" }\n",
			want:   "{ \"instruction\": \"read_file\" }\n",
		},
		{
			name:   "null tools",
			source: `{ "tools": null }`,
			want:   `{ "tools": null }`,
		},
		{
			name:   "already renamed",
			source: `{ "tools": {"storage_read":{}} }`,
			want:   `{ "tools": {"storage_read":{}} }`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, changed, err := renameStorageToolsJSONSource([]byte(test.source))
			if err != nil || changed != test.changed || string(got) != test.want {
				t.Fatalf("renamed = %q, changed=%t, err=%v; want %q, changed=%t",
					got, changed, err, test.want, test.changed)
			}
			again, changed, err := renameStorageToolsJSONSource(got)
			if err != nil || changed || string(again) != test.want {
				t.Fatalf("second run = %q, changed=%t, err=%v", again, changed, err)
			}
		})
	}
}

func TestStorageToolsMigrationRejectsAmbiguousJSONSource(t *testing.T) {
	for _, source := range []string{
		`{"tools":{"read_file":{},"storage_read":{}}}`,
		`{"tools":{"read_file":{},"storage_read":{"enabled":false}}}`,
		`{"tools":{"read_file":{},"read_file":{}}}`,
		`{"tools":{"read_file":{},"read_\u0066ile":{}}}`,
		`{"tools":{"read_file":{}},"tools":{"read_file":{}}}`,
		`{"tools":{"read_file":{}}} {}`,
		`{"tools":{"read_file":{}}`,
		`[]`,
	} {
		if _, _, err := renameStorageToolsJSONSource([]byte(source)); err == nil {
			t.Errorf("accepted ambiguous or invalid source %s", source)
		}
	}
}

func TestStorageToolsMigrationKeepsDistinctJSONSources(t *testing.T) {
	compiled := []byte(`{"instruction":"Example.","tools":{"read_file":{}}}`)
	hash, err := explicitDefaultToolsConfigHash(compiled)
	if err != nil {
		t.Fatal(err)
	}
	sources := []string{
		`{"instruction":"Example.","tools":{"read_file":{}}}`,
		`{"tools":{"read_file":{}},"instruction":"Example."}`,
		"{ \"instruction\": \"Example.\", \"tools\": {\"read_file\":{}} }\n",
	}
	seen := make(map[string]bool)
	var compiledHash string
	for _, source := range sources {
		config := storageToolConfig{
			compiled: compiled, hash: hash,
			source:     sql.NullString{String: source, Valid: true},
			format:     sql.NullString{String: "json", Valid: true},
			sourceHash: sql.NullString{String: hashBytes([]byte(source)), Valid: true},
		}
		migrated, changed, err := migrateStorageToolConfig(config)
		if err != nil || !changed {
			t.Fatalf("changed=%t err=%v", changed, err)
		}
		want := strings.ReplaceAll(source, "read_file", "storage_read")
		if migrated.source.String != want || migrated.sourceHash.String != hashBytes([]byte(want)) {
			t.Fatalf("source or hash changed beyond the tool key: %q", migrated.source.String)
		}
		if seen[migrated.sourceHash.String] {
			t.Fatal("distinct source text collapsed to the same source hash")
		}
		seen[migrated.sourceHash.String] = true
		if compiledHash != "" && compiledHash != migrated.hash {
			t.Fatal("equivalent configs have different compiled hashes")
		}
		compiledHash = migrated.hash
		if _, changed, err := migrateStorageToolConfig(migrated); err != nil || changed {
			t.Fatalf("second run changed=%t err=%v", changed, err)
		}
	}
}

func TestStorageToolsMigrationRejectsInheritedYAMLKeys(t *testing.T) {
	for _, source := range []string{
		"tools:\n  <<: {read_file: {enabled: false}}\n",
		"<<: {tools: {read_file: {}}}\n",
		"defaults: &defaults {read_file: {}}\ntools:\n  <<: *defaults\n",
		"defaults: &defaults {read_file: {}}\ntools: *defaults\n",
		"tools:\n  <<: {read_file: {}}\n  write_file: {}\n",
		"tools:\n  <<: {storage_read: {}}\n  read_file: {}\n",
	} {
		if _, _, err := renameStorageToolsYAML([]byte(source)); err == nil {
			t.Errorf("accepted inherited legacy tool keys in %q", source)
		}
	}
	// Unrelated merges need no inherited-key rewrite.
	source := "tools:\n  <<: {run_command: {}}\n  read_file: {}\n"
	want := "tools:\n  <<: {run_command: {}}\n  storage_read: {}\n"
	got, changed, err := renameStorageToolsYAML([]byte(source))
	if err != nil || !changed || string(got) != want {
		t.Fatalf("unrelated merge: got %q, changed=%t, err=%v", got, changed, err)
	}
}
