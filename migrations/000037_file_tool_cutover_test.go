package migrations

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"gopkg.in/yaml.v3"
)

func TestFileToolYAMLPreservesOtherValues(t *testing.T) {
	source := `instruction: |-
  Keep upload_artifact in this text.
model: {provider_config: openai-prod, name: "2026-09-14"}
tools:
  upload_artifact: &settings
    enabled: false
    permission: {mode: always_ask, parameters: {}}
  download_artifact: *settings
  run_command: *settings
`
	migrated, changed, err := renameFileToolsYAML([]byte(source))
	if err != nil || !changed {
		t.Fatalf("migration changed=%v err=%v", changed, err)
	}
	expectedSource := strings.ReplaceAll(source, "upload_artifact:", "upload_file:")
	expectedSource = strings.ReplaceAll(expectedSource, "download_artifact:", "download_file:")
	expected, err := decodeAgentConfigNameMigrationYAML([]byte(expectedSource))
	if err != nil {
		t.Fatal(err)
	}
	actual, err := decodeAgentConfigNameMigrationYAML(migrated)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("migration changed unrelated values: got %#v, want %#v", actual, expected)
	}
}

func TestFileToolYAMLFormatting(t *testing.T) {
	for _, style := range []string{"|", "|-", "|+", ">", ">-"} {
		t.Run(style, func(t *testing.T) {
			source := "# preserved comment\ninstruction: " + style + "\n" +
				"  First line.\n  Second line.\n\n  Another paragraph.\n\n" +
				"tools:\n  upload_artifact:\n    enabled: true\n"
			migrated, changed, err := renameFileToolsYAML([]byte(source))
			if err != nil || !changed {
				t.Fatalf("migration changed=%v err=%v", changed, err)
			}
			var before, after map[string]any
			if err := yaml.Unmarshal([]byte(source), &before); err != nil {
				t.Fatal(err)
			}
			if err := yaml.Unmarshal(migrated, &after); err != nil {
				t.Fatal(err)
			}
			if before["instruction"] != after["instruction"] {
				t.Fatalf("instruction changed from %q to %q", before["instruction"], after["instruction"])
			}
			if !strings.HasPrefix(string(migrated), "# preserved comment\n") ||
				!strings.Contains(string(migrated), "tools:\n  upload_file:\n    enabled: true\n") {
				t.Fatalf("unexpected YAML formatting:\n%s", migrated)
			}
		})
	}
}

func TestFileToolConfigMigration(t *testing.T) {
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			source := `{"instruction":"mention upload_artifact unchanged","tools":{"upload_artifact":{"enabled":false,"permission":{"mode":"always_ask","parameters":{}}},"download_artifact":{"permission":{"mode":"always_deny","parameters":{}}}}}`
			definition := []byte(source)
			if format == "yaml" {
				source = "# preserved comment\ninstruction: mention upload_artifact unchanged\n" +
					"tools:\n  upload_artifact:\n    enabled: false\n" +
					"    permission: {mode: always_ask, parameters: {}}\n" +
					"  download_artifact:\n    permission: {mode: always_deny, parameters: {}}\n"
			}
			compiled, err := canonicalJSON([]byte(
				`{"tools":{"upload_artifact":{"type":"built_in","enabled":false,"permission":{"mode":"always_ask","parameters":{}}},"download_artifact":{"type":"built_in","enabled":true,"permission":{"mode":"always_deny","parameters":{}}}}}`,
			))
			if err != nil {
				t.Fatal(err)
			}
			before := storedAgentConfig{id: "unchanged-id", source: source, sourceFormat: format,
				sourceHash: hashBytes([]byte(source)), definition: definition, compiledDefinition: compiled,
				effectiveDefinitionHash: hashBytes(compiled)}
			after, changed, err := migrateFileToolConfig(before)
			if err != nil || !changed {
				t.Fatalf("migration changed=%v err=%v", changed, err)
			}
			if after.id != before.id || after.sourceHash != hashBytes([]byte(after.source)) ||
				after.effectiveDefinitionHash != hashBytes(after.compiledDefinition) {
				t.Fatal("migration changed identity or left invalid hashes")
			}
			if !strings.Contains(after.source, "mention upload_artifact unchanged") ||
				(format == "yaml" && !strings.Contains(after.source, "# preserved comment")) {
				t.Fatal("migration changed unrelated source content")
			}
			var migrated agentconfig.Compiled
			if err := json.Unmarshal(after.compiledDefinition, &migrated); err != nil {
				t.Fatal(err)
			}
			if len(migrated.Tools) != 2 || migrated.Tools["upload_file"].Enabled ||
				migrated.Tools["upload_file"].Permission.Mode != "always_ask" ||
				migrated.Tools["download_file"].Permission.Mode != "always_deny" {
				t.Fatalf("migrated tools = %+v", migrated.Tools)
			}
			if _, err := agentconfig.RuntimeContractFromCompiled(after.compiledDefinition,
				agentconfig.CompilerVersion, after.effectiveDefinitionHash); err != nil {
				t.Fatalf("load migrated runtime: %v", err)
			}
			if _, changed, err := migrateFileToolConfig(after); err != nil || changed {
				t.Fatalf("second migration changed=%v err=%v", changed, err)
			}
		})
	}
}

func TestFileToolRenameConflicts(t *testing.T) {
	for _, rename := range []struct {
		name string
		run  func([]byte) ([]byte, bool, error)
	}{
		{"json", renameFileToolsJSON},
		{"yaml", renameFileToolsYAML},
	} {
		t.Run(rename.name, func(t *testing.T) {
			conflicting := []byte(`{"tools":{"upload_artifact":{"enabled":false},"upload_file":{"enabled":true}}}`)
			if _, _, err := rename.run(conflicting); err == nil {
				t.Fatal("accepted conflicting settings")
			}
			equivalent := []byte(`{"tools":{"upload_artifact":{"enabled":false},"upload_file":{"enabled":false}}}`)
			result, changed, err := rename.run(equivalent)
			if err != nil || !changed || strings.Contains(string(result), "upload_artifact") {
				t.Fatalf("collapse equivalent tools: %s changed=%v err=%v", result, changed, err)
			}
		})
	}
}

func TestFileToolConfigMigrationYAMLReferences(t *testing.T) {
	for _, toolSource := range []string{
		"<<: {tools: {upload_file: {}}}",
		"tools: {<<: {upload_file: {}}}",
		"tools: {<<: [{upload_file: {enabled: false}}, {upload_file: {enabled: true}}]}",
		"tools: {<<: {upload_file: {enabled: false}}, upload_file: {enabled: true}}",
		"tools: {upload_file: &settings {permission: {mode: always_ask}}, download_file: *settings}",
		"<<: {tools: &tools {upload_file: {}}}\ntools: *tools",
	} {
		t.Run(toolSource, func(t *testing.T) {
			source := "instruction: Help the user.\nmodel: {provider_config: openai-prod, name: gpt-test}\n" + toolSource + "\n"
			current, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{})
			if err != nil {
				t.Fatal(err)
			}
			legacyNames := strings.NewReplacer("upload_file", "upload_artifact", "download_file", "download_artifact")
			legacySource := legacyNames.Replace(source)
			var definition map[string]any
			if err := yaml.Unmarshal([]byte(legacySource), &definition); err != nil {
				t.Fatal(err)
			}
			definitionJSON, err := json.Marshal(definition)
			if err != nil {
				t.Fatal(err)
			}
			compiled := []byte(legacyNames.Replace(string(current.CanonicalJSON)))
			before := storedAgentConfig{source: legacySource, sourceFormat: "yaml",
				sourceHash: hashBytes([]byte(legacySource)), definition: definitionJSON,
				compiledDefinition: compiled, effectiveDefinitionHash: hashBytes(compiled)}
			after, changed, err := migrateFileToolConfig(before)
			if err != nil || !changed {
				t.Fatalf("migration changed=%v err=%v", changed, err)
			}
			recompiled, err := agentconfig.Compile(
				agentconfig.SourceFormatYAML, []byte(after.source), agentconfig.CompileOptions{},
			)
			if err != nil {
				t.Fatal(err)
			}
			if string(recompiled.CanonicalJSON) != string(current.CanonicalJSON) ||
				string(after.compiledDefinition) != string(current.CanonicalJSON) ||
				after.effectiveDefinitionHash != current.Hash || after.sourceHash != hashBytes([]byte(after.source)) {
				t.Fatal("migration changed config semantics or left inconsistent representations")
			}
			if _, changed, err := migrateFileToolConfig(after); err != nil || changed {
				t.Fatalf("second migration changed=%v err=%v", changed, err)
			}
			unchanged, changed, err := renameFileToolsYAML([]byte(source))
			if err != nil || changed || string(unchanged) != source {
				t.Fatalf("non-legacy source changed: changed=%v err=%v", changed, err)
			}
		})
	}
	_, _, err := renameFileToolsYAML([]byte(
		"tools: {<<: {upload_artifact: {enabled: false}}, upload_file: {enabled: true}}",
	))
	if err == nil {
		t.Fatal("accepted conflicting merged tool settings")
	}
}
