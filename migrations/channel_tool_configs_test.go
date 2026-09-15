package migrations

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelToolConfigMigrationPreservesOtherSettings(t *testing.T) {
	compiled := `{"instruction":"Keep send_integration_message in quoted instructions.",
"model":{"configured_model_id":"mdl_pinned"},
"machine_sources":[{"machine_pool_id":"pool_pinned","machine_provider_options_overlay":{"count":9007199254740993}}],
"tools":{"send_integration_message":{"enabled":true,"permission":{"mode":"always_allow"}},
"set_integration_target":{"enabled":false,"permission":{"mode":"always_allow"}},
"custom_task":{"type":"custom","enabled":true,"permission":{"mode":"always_ask"},
"input_schema":{"type":"object","properties":{"send_integration_message":{"type":"string"}}}}},
"mcp":{"server":{"url":"https://example.test/mcp","tools":{"set_integration_target":{"enabled":true}}}}}`
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			source := compiled
			if format == "yaml" {
				source = `# Preserve the human-authored comments.
instruction: |
  Keep send_integration_message in quoted instructions.
tools:
  send_integration_message:
    type: built_in
    permission: {mode: always_allow}
  set_integration_target: {enabled: false}
  custom_task:
    type: custom
    description: A custom task.
    input_schema: {type: object, properties: {send_integration_message: {type: string}}}
model: {provider_config: production, name: current-model}
mcp:
  server:
    url: https://example.test/mcp
    tools: {set_integration_target: {enabled: true}}
`
			}
			original := channelConfigFixture(t, source, format, compiled)
			got, changed, err := migrateChannelToolConfig(original)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, original.id, got.id)
			require.Equal(t, hashBytes([]byte(got.source)), got.sourceHash)
			compiledHash, err := channelToolCompiledHash(got.compiledDefinition)
			require.NoError(t, err)
			require.Equal(t, compiledHash, got.effectiveDefinitionHash)
			require.JSONEq(t, string(got.definition), string(got.compiledDefinition))
			before, err := decodeAgentConfigNameMigrationJSON(original.compiledDefinition)
			require.NoError(t, err)
			after, err := decodeAgentConfigNameMigrationJSON(got.compiledDefinition)
			require.NoError(t, err)
			root, ok := before.(map[string]any)
			require.True(t, ok)
			tools, ok := root["tools"].(map[string]any)
			require.True(t, ok)
			delete(tools, "send_integration_message")
			delete(tools, "set_integration_target")
			require.Equal(t, before, after, "preserve every other compiled value, including large integers")
			if format == "yaml" {
				require.Contains(t, got.source, "# Preserve the human-authored comments.")
			}
			replayed, changed, err := migrateChannelToolConfig(got)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, got, replayed)
		})
	}
}

func TestChannelToolConfigMigrationRetainsSameNamedCustomTool(t *testing.T) {
	raw := `{"instruction":"Retain custom tools.","tools":{
"send_integration_message":{"type":"custom","enabled":true,"input_schema":{"type":"object"}},
"set_integration_target":{"enabled":true}}}`
	original := channelConfigFixture(t, raw, "json", raw)
	got, changed, err := migrateChannelToolConfig(original)
	require.NoError(t, err)
	require.True(t, changed)
	require.JSONEq(t, `{"instruction":"Retain custom tools.","tools":{
"send_integration_message":{"type":"custom","enabled":true,"input_schema":{"type":"object"}}}}`,
		string(got.compiledDefinition))
	replayed, changed, err := migrateChannelToolConfig(got)
	require.NoError(t, err)
	require.False(t, changed)
	require.Equal(t, got, replayed)
}

func TestChannelToolConfigMigrationRejectsDamagedSource(t *testing.T) {
	raw := `{"instruction":"Keep settings.","tools":{"send_integration_message":{"enabled":true}}}`
	for _, test := range []struct {
		name string
		edit func(*storedAgentConfig)
	}{
		{"source hash", func(c *storedAgentConfig) { c.sourceHash = "wrong" }},
		{"compiled hash", func(c *storedAgentConfig) { c.effectiveDefinitionHash = "wrong" }},
		{"missing source declaration", func(c *storedAgentConfig) {
			c.source = `{"instruction":"Keep settings."}`
			c.sourceHash = hashBytes([]byte(c.source))
		}},
		{"mismatched definition", func(c *storedAgentConfig) { c.definition = []byte(`{"instruction":"Keep settings."}`) }},
		{"unsupported source", func(c *storedAgentConfig) { c.sourceFormat = "toml" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			original := channelConfigFixture(t, raw, "json", raw)
			test.edit(&original)
			got, changed, err := migrateChannelToolConfig(original)
			require.Error(t, err)
			require.False(t, changed)
			require.Equal(t, original, got)
		})
	}
}

func TestChannelToolConfigMigrationRemovesEmptyToolsAndRejectsAliasedTools(t *testing.T) {
	compiled := `{"instruction":"Keep settings.","tools":{"send_integration_message":{"enabled":true}}}`
	for _, source := range []string{
		"instruction: Keep settings.\ntools: {send_integration_message: {enabled: true}}\n",
		"instruction: Keep settings.\ntools:\n  send_integration_message: {enabled: true}\n",
	} {
		got, changed, err := migrateChannelToolConfig(channelConfigFixture(t, source, "yaml", compiled))
		require.NoError(t, err)
		require.True(t, changed)
		require.JSONEq(t, `{"instruction":"Keep settings."}`, string(got.compiledDefinition))
		require.NotContains(t, got.source, "tools:")
	}
	source := "other: &tools {send_integration_message: {enabled: true}}\ntools: *tools\n"
	original := channelConfigFixture(t, source, "yaml", compiled)
	_, changed, err := migrateChannelToolConfig(original)
	require.ErrorContains(t, err, "direct mapping")
	require.False(t, changed)
}

func channelConfigFixture(t *testing.T, source, format, compiled string) storedAgentConfig {
	t.Helper()
	canonical, err := canonicalJSON(json.RawMessage(compiled))
	require.NoError(t, err)
	compiledHash, err := channelToolCompiledHash(canonical)
	require.NoError(t, err)
	return storedAgentConfig{
		id: "original-config-id", source: source, sourceFormat: format,
		sourceHash: hashBytes([]byte(source)), definition: canonical,
		compiledDefinition: canonical, effectiveDefinitionHash: compiledHash,
	}
}
