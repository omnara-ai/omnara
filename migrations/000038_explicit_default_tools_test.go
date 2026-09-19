package migrations

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestExplicitDefaultToolsMigration(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, uuid.New())
	require.NoError(t, err)
	opts := agentconfig.CompileOptions{
		ResolveSkillID: func(id string) (agentconfig.SkillResolution, error) {
			return agentconfig.SkillResolution{PublicID: id, Name: "test-skill"}, nil
		},
	}
	for _, source := range []struct {
		format agentconfig.SourceFormat
		raw    string
	}{
		{agentconfig.SourceFormatJSON,
			`{"instruction":"a < b & c","model":{"provider_config":"openai","name":"test"},"skills":["` +
				skillID + `"],"tools":{"web_search":{"permission":{"mode":"always_ask"}}}}`},
		{agentconfig.SourceFormatYAML,
			"# keep\ninstruction: a < b & c\nmodel: {provider_config: openai, name: test}\nskills: [" +
				skillID + "]\ntools: {web_search: {permission: {mode: always_ask}}}\n"},
		{agentconfig.SourceFormatYAML,
			"instruction: Help\nmodel: {provider_config: openai, name: test}\nskills: [" +
				skillID + "]\n<<: {tools: &tools {web_search: {}}}\ntools: *tools\n"},
	} {
		t.Run(string(source.format), func(t *testing.T) {
			current, err := agentconfig.Compile(source.format, []byte(source.raw), opts)
			require.NoError(t, err)
			var legacy agentconfig.Compiled
			require.NoError(t, json.Unmarshal(current.CanonicalJSON, &legacy))
			delete(legacy.Tools, "skill")
			delete(legacy.Tools, "read_file")
			delete(legacy.Tools, "search_files")
			encoded, err := agentconfig.EncodeCompiled(legacy)
			require.NoError(t, err)
			before := storedAgentConfig{
				id: "unchanged", source: source.raw, sourceFormat: string(source.format),
				sourceHash: hashBytes([]byte(source.raw)), definition: encoded.CanonicalJSON,
				compiledDefinition: encoded.CanonicalJSON, effectiveDefinitionHash: encoded.Hash,
			}
			after, changed, err := migrateExplicitDefaultTools(before)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, before.source, after.source)
			require.Equal(t, before.sourceHash, after.sourceHash)
			require.Equal(t, before.id, after.id)
			require.Equal(t, current.CanonicalJSON, after.compiledDefinition)
			require.Equal(t, after.compiledDefinition, after.definition)
			require.Equal(t, current.Hash, after.effectiveDefinitionHash)
			contract, err := agentconfig.RuntimeContractFromCompiled(after.compiledDefinition,
				agentconfig.CompilerVersion, after.effectiveDefinitionHash)
			require.NoError(t, err)
			require.Len(t, contract.Tools, 5)
			require.Equal(t, "skill", contract.Tools[3].Name)
			require.Equal(t, "always_allow", contract.Tools[3].Permission.Mode)
			again, changed, err := migrateExplicitDefaultTools(after)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, after, again)
		})
	}
}

func TestExplicitDefaultToolsMigrationNoop(t *testing.T) {
	for _, raw := range []string{
		`{}`, `{"skills":null,"subagents":null}`, `{"skills":[],"subagents":{}}`,
		`{"machine_sources":[{"machine_pool_id":"pool"}]}`,
		`{"skills":[{"public_id":"skill"}],"tools":{"skill":{"enabled":false}}}`,
		`{"skills":[{"public_id":"skill"}],"tools":{"skill":{"enabled":true,"permission":{"mode":"always_ask"}},"read_file":{"enabled":false},"search_files":{"enabled":false}}}`,
		`{"subagents":{"worker":{"type":"self"}},"tools":{"spawn_agent":{"enabled":false},"read_agent":{"enabled":false},"send_agent_message":{"enabled":false},"stop_agent":{"enabled":false},"list_agents":{"enabled":false}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			before := storedAgentConfig{source: raw, compiledDefinition: []byte(raw)}
			after, changed, err := migrateExplicitDefaultTools(before)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, before, after)
		})
	}
}

func TestExplicitRetrievalToolsMigration(t *testing.T) {
	for _, extra := range []string{
		`"tools":{"web_fetch":{}}`,
		`"tools":{"custom":{"type":"custom","description":"Test","input_schema":{"type":"object"}}}`,
		`"tools":{"web_fetch":{},"read_file":{"enabled":false},"search_files":{"permission":{"mode":"always_ask"}}}`,
		`"mcp":{"docs":{"url":"https://example.com/mcp","default_enabled":false}}`,
		`"tools":{"web_fetch":{"enabled":false}}`,
	} {
		t.Run(extra, func(t *testing.T) {
			source := `{"instruction":"Help","model":{"provider_config":"openai","name":"test"},` + extra + `}`
			current, err := agentconfig.Compile(agentconfig.SourceFormatJSON, []byte(source), agentconfig.CompileOptions{})
			require.NoError(t, err)
			var original struct{ Tools map[string]json.RawMessage }
			require.NoError(t, json.Unmarshal([]byte(source), &original))
			var legacy agentconfig.Compiled
			require.NoError(t, json.Unmarshal(current.CanonicalJSON, &legacy))
			for _, name := range []string{"read_file", "search_files"} {
				if _, configured := original.Tools[name]; !configured {
					delete(legacy.Tools, name)
				}
			}
			encoded, err := agentconfig.EncodeCompiled(legacy)
			require.NoError(t, err)
			before := storedAgentConfig{source: source, sourceFormat: "json", sourceHash: hashBytes([]byte(source)),
				definition: encoded.CanonicalJSON, compiledDefinition: encoded.CanonicalJSON, effectiveDefinitionHash: encoded.Hash}
			after, _, err := migrateExplicitDefaultTools(before)
			require.NoError(t, err)
			require.Equal(t, before.source, after.source)
			require.Equal(t, before.sourceHash, after.sourceHash)
			require.Equal(t, current.CanonicalJSON, after.compiledDefinition)
			again, changed, err := migrateExplicitDefaultTools(after)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, after, again)
		})
	}
}

func TestExplicitDefaultToolsMigrationSubagents(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, uuid.New())
	require.NoError(t, err)
	opts := agentconfig.CompileOptions{
		ResolveSkillID: func(id string) (agentconfig.SkillResolution, error) {
			return agentconfig.SkillResolution{PublicID: id, Name: "test-skill"}, nil
		},
	}
	for _, test := range []struct {
		name   string
		format agentconfig.SourceFormat
		extra  string
	}{
		{"yaml subagents", agentconfig.SourceFormatYAML, "subagents: {worker: {type: self}}\n"},
		{"yaml mixed", agentconfig.SourceFormatYAML, "skills: [" + skillID + "]\nsubagents: {worker: {type: self}}\n" +
			"tools: {read_agent: {permission: {mode: always_ask}}, stop_agent: {enabled: false}}\n"},
		{"json subagents", agentconfig.SourceFormatJSON,
			`"subagents":{"worker":{"type":"self"}},"tools":{"spawn_agent":{"enabled":false}}`},
		{"json mixed", agentconfig.SourceFormatJSON, `"skills":["` + skillID + `"],"subagents":{"worker":{"type":"self"}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := "# keep\ninstruction: Help\nmodel: {provider_config: openai, name: test}\n" + test.extra
			if test.format == agentconfig.SourceFormatJSON {
				source = `{"instruction":"Help","model":{"provider_config":"openai","name":"test"},` + test.extra + `}`
			}
			current, err := agentconfig.Compile(test.format, []byte(source), opts)
			require.NoError(t, err)
			var legacy agentconfig.Compiled
			require.NoError(t, json.Unmarshal(current.CanonicalJSON, &legacy))
			for _, name := range append([]string{"skill", "read_file", "search_files"}, toolcatalog.SubagentToolNames()...) {
				tool := legacy.Tools[name]
				if tool.Enabled && tool.Permission.Mode == "always_allow" {
					delete(legacy.Tools, name)
				}
			}
			encoded, err := agentconfig.EncodeCompiled(legacy)
			require.NoError(t, err)
			before := storedAgentConfig{source: source, sourceFormat: string(test.format), sourceHash: hashBytes([]byte(source)),
				definition: encoded.CanonicalJSON, compiledDefinition: encoded.CanonicalJSON, effectiveDefinitionHash: encoded.Hash}
			after, changed, err := migrateExplicitDefaultTools(before)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, before.source, after.source)
			require.Equal(t, before.sourceHash, after.sourceHash)
			require.Equal(t, current.CanonicalJSON, after.compiledDefinition)
			require.Equal(t, current.CanonicalJSON, after.definition)
			require.Equal(t, current.Hash, after.effectiveDefinitionHash)
			again, changed, err := migrateExplicitDefaultTools(after)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, after, again)
		})
	}
}

func TestExplicitDefaultToolsMigrationPreservesOtherFields(t *testing.T) {
	raw := []byte(`{"skills":[{"public_id":"skill"}],"machine_sources":[{"machine_pool_id":"pool"}],` +
		`"future":{"large":9007199254740993},` +
		`"tools":{"run_command":{"enabled":false,"permission":{"mode":"always_ask"}}}}`)
	updated, additions, err := addExplicitDefaultTools(raw)
	require.NoError(t, err)
	require.Equal(t, []string{"skill", "read_file", "search_files"}, additions)
	var before, after map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &before))
	require.NoError(t, json.Unmarshal(updated, &after))
	var tools map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(after["tools"], &tools))
	require.Len(t, tools, 4)
	delete(tools, "skill")
	delete(tools, "read_file")
	delete(tools, "search_files")
	after["tools"], err = json.Marshal(tools)
	require.NoError(t, err)
	for key, value := range before {
		require.JSONEq(t, string(value), string(after[key]))
	}
	require.Contains(t, string(updated), "9007199254740993")
}

func TestExplicitDefaultToolsMigrationRejectsInconsistentConfig(t *testing.T) {
	compiled := []byte(`{"skills":[{"public_id":"skill"}]}`)
	source := `{"skills":["skill"]}`
	for _, test := range []struct {
		name   string
		mutate func(*storedAgentConfig)
	}{
		{"source hash", func(c *storedAgentConfig) { c.sourceHash = "wrong" }},
		{"compiled hash", func(c *storedAgentConfig) { c.effectiveDefinitionHash = "wrong" }},
		{"definition", func(c *storedAgentConfig) { c.definition = []byte(`{}`) }},
		{"different missing tools", func(c *storedAgentConfig) {
			c.definition = []byte(`{"subagents":{"worker":{"type":"self"}}}`)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			before := storedAgentConfig{source: source, sourceFormat: "json", sourceHash: hashBytes([]byte(source)),
				definition: compiled, compiledDefinition: compiled, effectiveDefinitionHash: hashBytes(compiled)}
			test.mutate(&before)
			after, changed, err := migrateExplicitDefaultTools(before)
			require.Error(t, err)
			require.False(t, changed)
			require.Equal(t, before, after)
		})
	}
}
