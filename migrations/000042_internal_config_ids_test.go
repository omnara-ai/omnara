package migrations

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestInternalCompiledIDs(t *testing.T) {
	modelID, machineID, poolID := uuid.New(), uuid.New(), uuid.New()
	childModelID := uuid.New()
	skillID, profileID, secretID := uuid.New(), uuid.New(), uuid.New()
	public := func(kind publicid.Kind, id uuid.UUID) string {
		value, err := publicid.Encode(kind, id)
		require.NoError(t, err)
		return value
	}
	source := `{"instruction":"Keep original source","model":{"provider_config":"test","name":"test"},` +
		`"event_webhook":{"url":"https://example.com/events","events":["model_output"],"signing_secret_id":"` + public(publicid.KindSecret, secretID) + `"},` +
		`"machine_sources":[{"machine_name":"byo","secret_env_overlay":{"TOKEN":"` + public(publicid.KindSecret, secretID) + `","REMOVE":null}},{"machine_pool_name":"pool"}],` +
		`"skills":["` + public(publicid.KindSkill, skillID) + `"],"subagents":{"worker":{"type":"profile","profile":"profile","model":{"provider_config":"test","name":"child"}},"self":{"type":"self","model":{"reasoning":{"effort":"low"}}}},` +
		`"mcp":{"docs":{"url":"https://example.com/mcp","auth":{"type":"bearer","secret_id":"` + public(publicid.KindSecret, secretID) + `"}}},` +
		`"tools":{"skill":{"enabled":false},"custom":{"type":"custom","description":"Keep","input_schema":{"type":"object","properties":{"x":{"type":"number","minimum":1e-7,"maximum":1e21}}}}}}`
	current, err := agentconfig.Compile(agentconfig.SourceFormatJSON, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(_, name string) (agentconfig.ResolvedModelSelection, error) {
			if name == "child" {
				return agentconfig.ResolvedModelSelection{ConfiguredModelID: childModelID}, nil
			}
			return agentconfig.ResolvedModelSelection{ConfiguredModelID: modelID}, nil
		},
		ResolveMachineName:     func(string) (uuid.UUID, error) { return machineID, nil },
		ResolveMachinePoolName: func(string) (uuid.UUID, error) { return poolID, nil },
		ResolveSkillID: func(string) (agentconfig.SkillResolution, error) {
			return agentconfig.SkillResolution{ID: skillID, Name: "test"}, nil
		},
		ResolveAgentProfileName: func(string) (uuid.UUID, error) { return profileID, nil },
	})
	require.NoError(t, err)
	require.Equal(t, source, current.Source)
	legacy := strings.NewReplacer(
		`"configured_model_id":"`+childModelID.String()+`"`, `"provider_config":"test","name":"child"`,
		machineID.String(), public(publicid.KindMachine, machineID),
		poolID.String(), public(publicid.KindMachinePool, poolID),
		`"id":"`+skillID.String()+`"`, `"public_id":"`+public(publicid.KindSkill, skillID)+`"`,
		profileID.String(), public(publicid.KindAgentProfile, profileID),
		secretID.String(), public(publicid.KindSecret, secretID),
	).Replace(string(current.CanonicalJSON))
	raw := []byte(legacy)
	converted, err := internalCompiledIDs(raw, modelID, func(
		key string, profile uuid.UUID, provider, name string,
	) (uuid.UUID, error) {
		require.Equal(t, "worker", key)
		require.Equal(t, profileID, profile)
		require.Equal(t, "test", provider)
		require.Equal(t, "child", name)
		return childModelID, nil
	})
	require.NoError(t, err)
	require.Equal(t, string(current.CanonicalJSON), string(converted))
	hash, err := explicitDefaultToolsConfigHash(converted)
	require.NoError(t, err)
	require.Equal(t, current.Hash, hash)
	_, err = agentconfig.RuntimeContractFromCompiled(converted, hash)
	require.NoError(t, err)
}

func TestInternalCompiledIDsPreservesUnknownFields(t *testing.T) {
	id := uuid.New()
	raw := []byte(`{"model":{"configured_model_id":"` + id.String() + `"},` +
		`"future":{"integer":9007199254740993,"decimal":1.234567890123456789},"instruction":"sec_unchanged"}`)
	converted, err := internalCompiledIDs(raw, id, nil)
	require.NoError(t, err)
	require.Contains(t, string(converted), `9007199254740993`)
	require.Contains(t, string(converted), `1.234567890123456789`)
	require.Contains(t, string(converted), `sec_unchanged`)
}

func TestInternalCompiledIDsRejectsInvalidReferences(t *testing.T) {
	id := uuid.New()
	wrongKind, err := publicid.Encode(publicid.KindMachine, uuid.New())
	require.NoError(t, err)
	for _, extra := range []string{
		`"skills":[{}]`, `"skills":[{"public_id":"invalid"}]`, `"skills":[{"id":"already_internal"}]`,
		`"machine_sources":[{"machine_id":"invalid"}]`, `"machine_sources":[{"secret_env_overlay":[]} ]`,
		`"machine_sources":[{"secret_env_overlay":{"TOKEN":"` + wrongKind + `"}}]`,
		`"subagents":{"worker":{"profile_id":"invalid"}}`, `"mcp":{"docs":{"auth":[]}}`,
		`"mcp":{"docs":{"auth":{"secret_id":"invalid"}}}`, `"skills":[null]`,
		`"subagents":{"worker":{"model":[]}}`,
		`"subagents":{"worker":{"model":{"name":42}}}`,
		`"subagents":{"worker":{"model":{"provider_config":""}}}`,
	} {
		t.Run(extra, func(t *testing.T) {
			_, err := internalCompiledIDs([]byte(`{"model":{"configured_model_id":"`+id.String()+`"},`+extra+`}`), id, nil)
			require.Error(t, err)
		})
	}
	_, err = internalCompiledIDs([]byte(`{"model":{"configured_model_id":"`+id.String()+`"}}`), uuid.New(), nil)
	require.ErrorContains(t, err, "does not match")
}

func TestSubagentModelSourceDefaults(t *testing.T) {
	for _, test := range []struct {
		name, format, source, want string
	}{
		{
			name: "block", format: "yaml",
			source: "# Keep\nsubagents:\n    worker:\n        model:\n            name: child # Keep too\n",
			want: "# Keep\nsubagents:\n    worker:\n        model:\n" +
				"            \"provider_config\": \"test\"\n            name: child # Keep too\n",
		},
		{
			name: "unicode flow", format: "yaml",
			source: `instruction: 😃` + "\n" + `subagents: {worker: {model: {name: child}}}`,
			want:   `instruction: 😃` + "\n" + `subagents: {worker: {model: {"provider_config": "test", name: child}}}`,
		},
		{
			name: "JSON precision", format: "json",
			source: `{"other":[9007199254740993,1.234567890123456789,"😃 <>&"],` +
				`"subagents":{"worker":{"model":{"name":"child"}}}}`,
			want: `{"other":[9007199254740993,1.234567890123456789,"😃 <>&"],` +
				`"subagents":{"worker":{"model":{"provider_config": "test", "name":"child"}}}}`,
		},
		{
			name: "CRLF", format: "yaml",
			source: "subagents:\r\n  worker:\r\n    model:\r\n      name: child\r\n",
			want:   "subagents:\r\n  worker:\r\n    model:\r\n      \"provider_config\": \"test\"\r\n      name: child\r\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := addSubagentModelSourceDefaults([]byte(test.source), test.format,
				map[string]map[string]string{"worker": {"provider_config": "test"}})
			require.NoError(t, err)
			require.Equal(t, test.want, string(got))
		})
	}
}

func TestSubagentModelSourceDefaultsIsolateAliases(t *testing.T) {
	for _, source := range []string{
		"subagents:\n  worker:\n    model: &shared {name: child}\n  other:\n    model: *shared\n",
		"shared: &shared {name: child}\nsubagents:\n  worker:\n    model: *shared\n  other:\n    model: *shared\n",
		"shared: &shared {model: {name: child}}\nsubagents:\n  worker:\n    <<: *shared\n  other: *shared\n",
	} {
		before, err := decodeAgentConfigNameMigrationYAML([]byte(source))
		require.NoError(t, err)
		root, ok := before.(map[string]any)
		require.True(t, ok)
		subagents, ok := root["subagents"].(map[string]any)
		require.True(t, ok)
		worker, ok := subagents["worker"].(map[string]any)
		require.True(t, ok)
		model, ok := worker["model"].(map[string]any)
		require.True(t, ok)
		model["provider_config"] = "test"
		got, err := addSubagentModelSourceDefaults([]byte(source), "yaml",
			map[string]map[string]string{"worker": {"provider_config": "test"}})
		require.NoError(t, err)
		after, err := decodeAgentConfigNameMigrationYAML(got)
		require.NoError(t, err)
		require.Equal(t, before, after)
	}
}
