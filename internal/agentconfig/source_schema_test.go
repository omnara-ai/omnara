package agentconfig

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestParseSourceRejectsOversizedMachineResources(t *testing.T) {
	for _, field := range []string{"machine_cpu", "machine_memory_mb"} {
		t.Run(field, func(t *testing.T) {
			source := validAgentSource(fmt.Sprintf(`
machine_sources:
  - machine_pool_name: Build Pool
    %s: %d
`, field, int64(math.MaxInt32)+1))
			if _, err := ParseSource(SourceFormatYAML, []byte(source)); err == nil {
				t.Fatalf("expected oversized %s to be rejected", field)
			}
		})
	}
}

func TestParseSourceValidatesMachineTextLengths(t *testing.T) {
	for _, sourceField := range []string{"machine_name", "machine_pool_name"} {
		for _, field := range []string{"description", "cwd"} {
			for _, length := range []int{4096, 4097} {
				t.Run(fmt.Sprintf("%s/%s/%d", sourceField, field, length), func(t *testing.T) {
					source := validAgentSource(fmt.Sprintf(`
machine_sources:
  - %s: Build Machine
    %s: %q
`, sourceField, field, strings.Repeat("é", length)))
					_, err := ParseSource(SourceFormatYAML, []byte(source))
					if length == 4096 {
						require.NoError(t, err)
					} else {
						require.ErrorContains(t, err, field)
					}
				})
			}
		}
	}
}

func TestParseSourceValidatesIdleDeletionMinutes(t *testing.T) {
	for _, test := range []struct {
		minutes int
		valid   bool
	}{
		{minutes: 0, valid: true},
		{minutes: 4, valid: false},
		{minutes: 5, valid: true},
	} {
		source := validAgentSource(fmt.Sprintf(`
machine_sources:
  - machine_pool_name: Build Pool
    delete_after_idle_minutes: %d
`, test.minutes))
		_, err := ParseSource(SourceFormatYAML, []byte(source))
		if (err == nil) != test.valid {
			t.Fatalf("delete_after_idle_minutes %d error = %v, valid = %v", test.minutes, err, test.valid)
		}
	}
}

func TestParseSourceRejectsInvalidResourceNameReferences(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name: "provider config boundary whitespace",
			source: `
instruction: Help the user make progress.
model:
  provider_config: " openai-prod"
  name: gpt-test
`,
			want: "provider_config",
		},
		{
			name: "configured model boundary whitespace",
			source: `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: "gpt-test "
`,
			want: "model.name",
		},
		{
			name: "machine boundary whitespace",
			source: validAgentSource(`
machine_sources:
  - machine_name: " Primary Machine"
`),
			want: "machine_name",
		},
		{
			name: "machine pool boundary whitespace",
			source: validAgentSource(`
machine_sources:
  - machine_pool_name: "Build Pool "
`),
			want: "machine_pool_name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseSource(SourceFormatYAML, []byte(tt.source))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("ParseSource error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestParseSourceNormalizesResourceNameReferencesBeforeLengthValidation(t *testing.T) {
	decomposed := strings.Repeat("e\u0301", 64)
	source := fmt.Sprintf(`
instruction: Help the user make progress.
model:
  provider_config: %q
  name: %q
machine_sources:
  - machine_pool_name: %q
`, decomposed, "Cafe\u0301", "Build Cafe\u0301")
	parsed, err := ParseSource(SourceFormatYAML, []byte(source))
	require.NoError(t, err)
	if parsed.Model.ProviderConfig != strings.Repeat("é", 64) ||
		parsed.Model.Name != "Café" ||
		parsed.MachineSources[0].MachinePoolName != "Build Café" {
		t.Fatalf("normalized references = %+v", parsed)
	}
}

func TestParseSourceRejectsUnknownFieldsWithJSONSchema(t *testing.T) {
	for name, source := range map[string]string{
		"top_level": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
unknown: true
`,
		"model": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
  unknown: true
`,
		"machine": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
machine:
  grant_id: pmg_test
  unknown: true
`,
		"tool": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  run_command:
    unknown: true
`,
		"mcp_server": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
mcp:
  docs:
    url: https://example.com/mcp
    unknown: true
`,
		"mcp_auth": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
mcp:
  docs:
    url: https://example.com/mcp
    auth:
      type: bearer
      secret_id: sec_aaaaaaaaaaaaaaaaaaaaaaaaaa
      unknown: true
`,
		"mcp_tool": `
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
mcp:
  docs:
    url: https://example.com/mcp
    tools:
      search:
        unknown: true
`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseSource(SourceFormatYAML, []byte(source))
			if err == nil {
				t.Fatal("expected JSON Schema validation to reject the unknown field")
			}
			if !strings.Contains(err.Error(), "unknown field") {
				t.Fatalf("expected unknown field error, got %v", err)
			}
		})
	}
}

// TestSourceSchemaIsAtLeastAsStrictAsGoStructs enforces that schema
// validation rejects anything the Go struct decode would not represent:
// ParseSource decodes with plain json.Unmarshal, so the schema is the only
// unknown-field gate. Every schema object backed by a Go struct must set
// additionalProperties=false and declare exactly the struct's JSON fields —
// an extra schema property would be silently dropped on decode, and a
// missing one would reject valid configs.
func TestSourceSchemaIsAtLeastAsStrictAsGoStructs(t *testing.T) {
	schemaJSON, err := SourceJSONSchema()
	if err != nil {
		t.Fatalf("generate source schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatalf("decode source schema: %v", err)
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatal("source schema has no $defs object")
	}
	structsByDef := map[string]reflect.Type{
		"AgentConfigDefinitionGitCredentials":      reflect.TypeOf(GitCredentialsSource{}),
		"AgentConfigDefinitionInteractionHandler":  reflect.TypeOf(AgentConfigIntegrationCapabilitySource{}),
		"AgentConfigDefinitionModel":               reflect.TypeOf(AgentConfigModelSource{}),
		"AgentConfigDefinitionMachineSource":       reflect.TypeOf(AgentConfigMachineSource{}),
		"AgentConfigDefinitionTool":                reflect.TypeOf(AgentConfigToolSource{}),
		"AgentConfigDefinitionMCPServer":           reflect.TypeOf(AgentConfigMCPSource{}),
		"AgentConfigDefinitionMCPAuth":             reflect.TypeOf(AgentConfigMCPAuthSource{}),
		"AgentConfigDefinitionMCPTool":             reflect.TypeOf(AgentConfigMCPToolSource{}),
		"AgentConfigDefinitionToolPermission":      reflect.TypeOf(toolpermission.Selection{}),
		"AgentConfigDefinitionSubagent":            reflect.TypeOf(AgentConfigSubagentSource{}),
		"AgentConfigDefinitionSubagentModel":       reflect.TypeOf(AgentConfigSubagentModelSource{}),
		"AgentConfigDefinitionSubagentInstruction": reflect.TypeOf(AgentConfigSubagentInstructionSource{}),
		"AgentConfigDefinitionMemoryStore":         reflect.TypeOf(MemoryStoreSource{}),
	}
	openDefs := map[string]bool{
		"AgentConfigDefinitionToolInputSchema":     true,
		"AgentConfigDefinitionMachineSources":      true,
		"AgentConfigDefinitionInteractionHandlers": true,
		"AgentConfigDefinitionMCPServers":          true,
		"AgentConfigDefinitionMemoryStores":        true,
		"AgentConfigDefinitionSkills":              true,
		"AgentConfigDefinitionSubagents":           true,
		"AgentConfigDefinitionTools":               true,
		"EnvironmentVariableName":                  true,
		"MemoryStoreName":                          true,
		"ResourceNameReference":                    true,
	}
	for name := range defs {
		if !openDefs[name] && structsByDef[name] == nil {
			t.Errorf("schema $def %q is not mapped to a Go struct here; map it or mark it open", name)
		}
	}
	testutil.AssertSchemaMatchesStruct(t, "root", schema, reflect.TypeOf(AgentConfigSource{}))
	for name, structType := range structsByDef {
		def, ok := defs[name].(map[string]any)
		if !ok {
			t.Errorf("schema $def %q is missing", name)
			continue
		}
		testutil.AssertSchemaMatchesStruct(t, name, def, structType)
	}
}

func TestParseSourceRejectsInvalidShapeBeforeCompilerValidation(t *testing.T) {
	_, err := ParseSource(SourceFormatJSON, []byte(`{
		"instruction": "Help the user make progress.",
		"model": {"provider_config": 123, "id": "gpt-test"}
	}`))
	if err == nil {
		t.Fatal("expected JSON Schema validation to reject an invalid model.provider_config type")
	}
	var validationErr *ValidationError
	if !errors.As(err, &validationErr) {
		t.Fatalf("expected JSON Schema validation error, got %v", err)
	}
}

func TestParseSourceValidatesMachineSourceEnvNames(t *testing.T) {
	for name, source := range map[string]string{
		"env_empty": `
machine_sources:
  - machine_pool_name: Build Pool
    env_overlay:
      "": value
`,
		"env_equals": `
machine_sources:
  - machine_pool_name: Build Pool
    env_overlay:
      "BAD=KEY": value
`,
		"secret_env_empty": `
machine_sources:
  - machine_pool_name: Build Pool
    secret_env_overlay:
      "": sec_aaaaaaaaaaaaaaaaaaaaaaaaaa
`,
		"secret_env_equals": `
machine_sources:
  - machine_pool_name: Build Pool
    secret_env_overlay:
      "BAD=KEY": sec_aaaaaaaaaaaaaaaaaaaaaaaaaa
`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseSource(SourceFormatYAML, []byte(validAgentSource(source))); err == nil {
				t.Fatal("expected invalid env name to be rejected")
			}
		})
	}

	_, err := ParseSource(SourceFormatYAML, []byte(validAgentSource(`
machine_sources:
  - machine_pool_name: Build Pool
    env_overlay:
      "1.lower-name has space": value
    secret_env_overlay:
      "lower.name": sec_aaaaaaaaaaaaaaaaaaaaaaaaaa
`)))
	if err != nil {
		t.Fatalf("parse source with raw process env names: %v", err)
	}
}
