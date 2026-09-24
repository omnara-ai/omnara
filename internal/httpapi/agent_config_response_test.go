package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestPublicCompiledDefinition(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	public := func(kind publicid.Kind) string {
		value, err := publicid.Encode(kind, id)
		require.NoError(t, err)
		return value
	}
	source := strings.ReplaceAll(`{
		"version":"v1","instruction":"11111111-1111-4111-8111-111111111111","max_depth":2,"max_subagents":4,
		"model":{"configured_model_id":"UUID","context_window_tokens":32000,"default_max_output_tokens":1000,"cache_retention":"long","reasoning":{"effort":"high"}},
		"machine_sources":[
			{"machine_id":"UUID","description":"BYO"},
			{"machine_pool_id":"UUID","max_machines":3,"initial_num_machines":1,"delete_after_idle_minutes":0,
			"cwd":"/app","machine_cpu":2,"machine_memory_mb":1024,
			"env_overlay":{"KEEP":"value","REMOVE":null},"secret_env_overlay":{"TOKEN":"UUID","REMOVE":null},
			"machine_provider_options_overlay":{"count":18446744073709551615,"precision":1.234567890123456789,"unset":null}}
		],
		"tools":{"custom":{"enabled":true,"type":"custom","permission":{"mode":"always_allow","parameters":{"count":9007199254740993}},"deferred":true,"description":"a < b & c",
			"input_schema":{"type":"object","properties":{"value":{"const":18446744073709551615},"number":{"const":1.234567890123456789}}}},
			"skill":{"enabled":false,"type":"built_in","permission":{"mode":"always_allow","parameters":{}}},
			"int__chat__post_message":{"integration_id":"UUID","enabled":true,"permission":{"mode":"always_ask","parameters":{}},"deferred":true},
			"int__chat__read":{"integration_id":"UUID","enabled":false,"permission":{"mode":"always_allow","parameters":{}}}},
		"interaction_handlers":{"chat":{"integration_id":"UUID"}},
		"mcp":{"docs":{"url":"https://example.com","default_enabled":false,"permission":{"mode":"always_ask","parameters":{}},"deferred":true,
			"auth":{"type":"sigv4","secret_id":"UUID","region":"us-west-2","service":"execute-api"},
			"tools":{"read":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}},"deferred":false},"inherit":{}}},
			"bearer":{"url":"https://example.org","default_enabled":true,"permission":{"mode":"always_allow","parameters":{}},"auth":{"type":"bearer","secret_id":"UUID"}},
			"oauth":{"url":"https://example.org","default_enabled":true,"permission":{"mode":"always_allow","parameters":{}},"auth":{"type":"oauth","secret_id":"UUID"}},
			"public":{"url":"https://example.org","default_enabled":true,"permission":{"mode":"always_allow","parameters":{}}}},
		"event_webhook":{"url":"https://example.com/events","events":["model_output"],"signing_secret_id":"UUID"},
		"skills":[{"id":"UUID"}],
		"subagents":{"self":{"type":"self","description":"self","instruction_append":"more","max_instances":2,"archive_after_idle_minutes":0,"model":{"reasoning":{"effort":"low"}}},
			"profile":{"type":"profile","profile_id":"UUID","description":"worker","instruction_append":"more","max_instances":3,"archive_after_idle_minutes":0,
			"model":{"configured_model_id":"UUID","context_window_tokens":64000,"default_max_output_tokens":2000,"cache_retention":"short","reasoning":{"effort":"low"}}}}
	}`, "UUID", id.String())
	raw := json.RawMessage(source)
	projected, err := publicCompiledDefinition(raw)
	require.NoError(t, err)
	require.Equal(t, source, string(raw))
	actual, err := json.Marshal(projected)
	require.NoError(t, err)
	expected := strings.NewReplacer(
		`"configured_model_id":"`+id.String()+`"`, `"configured_model_id":"`+public(publicid.KindConfiguredModel)+`"`,
		`"machine_id":"`+id.String()+`"`, `"machine_id":"`+public(publicid.KindMachine)+`"`,
		`"machine_pool_id":"`+id.String()+`"`, `"machine_pool_id":"`+public(publicid.KindMachinePool)+`"`,
		`"TOKEN":"`+id.String()+`"`, `"TOKEN":"`+public(publicid.KindSecret)+`"`,
		`"secret_id":"`+id.String()+`"`, `"secret_id":"`+public(publicid.KindSecret)+`"`,
		`"signing_secret_id":"`+id.String()+`"`, `"signing_secret_id":"`+public(publicid.KindSecret)+`"`,
		`"id":"`+id.String()+`"`, `"id":"`+public(publicid.KindSkill)+`"`,
		`"profile_id":"`+id.String()+`"`, `"profile_id":"`+public(publicid.KindAgentProfile)+`"`,
		`"integration_id":"`+id.String()+`"`, `"integration_id":"`+public(publicid.KindProjectIntegration)+`"`,
	).Replace(source)
	decode := func(raw []byte) any {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		require.NoError(t, decoder.Decode(&value))
		return value
	}
	require.Equal(t, decode([]byte(expected)), decode(actual))
	require.Equal(t, publicid.ID(public(publicid.KindConfiguredModel)), projected.Model.ConfiguredModelId)
	spec, err := openapi.GetSpec()
	require.NoError(t, err)
	var value any
	require.NoError(t, json.Unmarshal(actual, &value))
	require.NoError(t, spec.Components.Schemas["CompiledAgentConfig"].Value.VisitJSON(value))
}

func TestCompiledConfigEnums(t *testing.T) {
	t.Parallel()
	spec, err := openapi.GetSpec()
	require.NoError(t, err)
	for _, test := range []struct {
		schema, field string
		values        []any
	}{
		{"CompiledAgentModel", "cache_retention", []any{"none", "short", "long"}},
		{"CompiledSubagentModel", "cache_retention", []any{"none", "short", "long"}},
		{"CompiledTool", "type", []any{"built_in", "custom"}},
	} {
		t.Run(test.schema+"/"+test.field, func(t *testing.T) {
			t.Parallel()
			field := spec.Components.Schemas[test.schema].Value.Properties[test.field].Value
			require.ElementsMatch(t, test.values, field.Enum)
			require.Error(t, field.VisitJSON("unsupported"))
		})
	}
}

func TestCompiledConfigVariants(t *testing.T) {
	t.Parallel()
	spec, err := openapi.GetSpec()
	require.NoError(t, err)
	profileID, err := publicid.Encode(publicid.KindAgentProfile, uuid.New())
	require.NoError(t, err)
	secretID, err := publicid.Encode(publicid.KindSecret, uuid.New())
	require.NoError(t, err)
	integrationID, err := publicid.Encode(publicid.KindProjectIntegration, uuid.New())
	require.NoError(t, err)
	for _, test := range []struct {
		schema, raw string
		valid       bool
	}{
		{"CompiledIntegrationCapability", `{"integration_id":"INTEGRATION"}`, true},
		{"CompiledIntegrationCapability", `{}`, false},
		{"CompiledIntegrationCapability", `{"integration_id":"11111111-1111-4111-8111-111111111111"}`, false},
		{"CompiledIntegrationCapability", `{"integration_id":"PROFILE"}`, false},
		{
			"CompiledTool",
			`{"integration_id":"INTEGRATION","enabled":true,"permission":{"mode":"always_allow","parameters":{}}}`,
			true,
		},
		{"CompiledTool", `{"integration_id":"11111111-1111-4111-8111-111111111111","enabled":true,
            "permission":{"mode":"always_allow","parameters":{}}}`, false},
		{"CompiledSubagent", `{"type":"self"}`, true},
		{"CompiledSubagent", `{"type":"profile","profile_id":"PROFILE"}`, true},
		{"CompiledSubagent", `{"type":"profile"}`, false},
		{"CompiledSubagent", `{"type":"self","profile_id":"PROFILE"}`, false},
		{"CompiledSubagent", `{"type":"unknown"}`, false},
		{"CompiledMCPAuth", `{"type":"bearer","secret_id":"SECRET"}`, true},
		{"CompiledMCPAuth", `{"type":"oauth","secret_id":"SECRET"}`, true},
		{"CompiledMCPAuth", `{"type":"sigv4","secret_id":"SECRET","service":"s3","region":"us-west-2"}`, true},
		{"CompiledMCPAuth", `{"type":"sigv4","secret_id":"SECRET","region":"us-west-2"}`, false},
		{"CompiledMCPAuth", `{"type":"sigv4","secret_id":"SECRET","service":"s3"}`, false},
		{"CompiledMCPAuth", `{"type":"sigv4","secret_id":"SECRET","service":"","region":"us-west-2"}`, false},
		{"CompiledMCPAuth", `{"type":"sigv4","secret_id":"SECRET","service":"s3","region":""}`, false},
		{"CompiledMCPAuth", `{"type":"bearer","secret_id":"SECRET","service":"s3"}`, false},
		{"CompiledMCPAuth", `{"type":"oauth","secret_id":"SECRET","region":"us-west-2"}`, false},
		{"CompiledMCPAuth", `{"type":"bearer"}`, false},
		{"CompiledMCPAuth", `{"type":"unknown","secret_id":"SECRET"}`, false},
	} {
		t.Run(test.schema+"/"+test.raw, func(t *testing.T) {
			t.Parallel()
			raw := strings.NewReplacer("PROFILE", profileID, "SECRET", secretID, "INTEGRATION", integrationID).Replace(test.raw)
			var value any
			require.NoError(t, json.Unmarshal([]byte(raw), &value))
			err := spec.Components.Schemas[test.schema].Value.VisitJSON(value)
			if test.valid {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestAgentConfigSummaryAndDetailSchemas(t *testing.T) {
	t.Parallel()
	spec, err := openapi.GetSpec()
	require.NoError(t, err)
	schemas := spec.Components.Schemas
	require.NotContains(t, schemas["AgentConfigSummary"].Value.Properties, "compiled_definition")
	require.Contains(t, schemas["AgentConfig"].Value.Required, "compiled_definition")
	require.Equal(t, "#/components/schemas/CompiledAgentConfig",
		schemas["AgentConfig"].Value.Properties["compiled_definition"].Ref)
	require.Equal(t, "#/components/schemas/AgentConfigSummary",
		schemas["AgentProfileSummary"].Value.Properties["current_config"].Ref)
	require.Equal(t, "#/components/schemas/AgentConfig", schemas["AgentProfile"].Value.Properties["current_config"].Ref)
}

func TestPublicCompiledDefinitionInvalid(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		`{`, `null`, `{}`, `{"model":null}`, `{"model":[]}`,
		`{"model":{"configured_model_id":"not-a-uuid"}}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"skills":[{"id":"not-a-uuid"}]}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"subagents":{"child":{"type":"unknown"}}}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"subagents":{"child":{"type":"profile"}}}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"mcp":{"docs":{"auth":{"type":"unknown","secret_id":"11111111-1111-4111-8111-111111111111"}}}}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"interaction_handlers":{"chat":{}}}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"interaction_handlers":{"chat":{"integration_id":"not-a-uuid"}}}`,
		`{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111"},"tools":{"int__chat__read":{"integration_id":"not-a-uuid"}}}`,
	} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			_, err := publicCompiledDefinition(json.RawMessage(source))
			require.Error(t, err)
		})
	}
}

func TestPublicCompiledDefinitionOmitsAbsentFields(t *testing.T) {
	t.Parallel()
	id := uuid.New()
	encoded, err := publicid.Encode(publicid.KindConfiguredModel, id)
	require.NoError(t, err)
	projected, err := publicCompiledDefinition(json.RawMessage(`{"instruction":"Hello","model":{"configured_model_id":"` + id.String() + `"}}`))
	require.NoError(t, err)
	raw, err := json.Marshal(projected)
	require.NoError(t, err)
	require.JSONEq(t, `{"instruction":"Hello","model":{"configured_model_id":"`+encoded+`"}}`, string(raw))
}
