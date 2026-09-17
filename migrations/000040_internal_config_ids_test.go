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
	skillID, profileID, secretID := uuid.New(), uuid.New(), uuid.New()
	public := func(kind publicid.Kind, id uuid.UUID) string {
		value, err := publicid.Encode(kind, id)
		require.NoError(t, err)
		return value
	}
	source := `{"instruction":"Keep original source","model":{"provider_config":"test","name":"test"},` +
		`"machine_sources":[{"machine_name":"byo","secret_env_overlay":{"TOKEN":"` + public(publicid.KindSecret, secretID) + `","REMOVE":null}},{"machine_pool_name":"pool"}],` +
		`"skills":["` + public(publicid.KindSkill, skillID) + `"],"subagents":{"worker":{"type":"profile","profile":"profile"},"self":{"type":"self"}},` +
		`"mcp":{"docs":{"url":"https://example.com/mcp","auth":{"type":"bearer","secret_id":"` + public(publicid.KindSecret, secretID) + `"}}},` +
		`"tools":{"skill":{"enabled":false},"custom":{"type":"custom","description":"Keep","input_schema":{"type":"object","properties":{"x":{"type":"number","minimum":1e-7,"maximum":1e21}}}}}}`
	current, err := agentconfig.Compile(agentconfig.SourceFormatJSON, []byte(source), agentconfig.CompileOptions{
		ResolveModelSelection: func(string, string) (agentconfig.ResolvedModelSelection, error) {
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
		machineID.String(), public(publicid.KindMachine, machineID),
		poolID.String(), public(publicid.KindMachinePool, poolID),
		`"id":"`+skillID.String()+`"`, `"public_id":"`+public(publicid.KindSkill, skillID)+`"`,
		profileID.String(), public(publicid.KindAgentProfile, profileID),
		secretID.String(), public(publicid.KindSecret, secretID),
	).Replace(string(current.CanonicalJSON))
	raw := []byte(legacy)
	converted, err := internalCompiledIDs(raw, modelID)
	require.NoError(t, err)
	require.Equal(t, string(current.CanonicalJSON), string(converted))
	hash, err := explicitDefaultToolsConfigHash(converted)
	require.NoError(t, err)
	require.Equal(t, current.Hash, hash)
	_, err = agentconfig.RuntimeContractFromCompiled(converted, agentconfig.CompilerVersion, hash)
	require.NoError(t, err)
	_, err = agentconfig.RuntimeContractFromCompiled(raw, "", hash)
	require.ErrorContains(t, err, "not supported")
}

func TestInternalCompiledIDsPreservesUnknownFields(t *testing.T) {
	id := uuid.New()
	raw := []byte(`{"model":{"configured_model_id":"` + id.String() + `"},` +
		`"future":{"integer":9007199254740993,"decimal":1.234567890123456789},"instruction":"sec_unchanged"}`)
	converted, err := internalCompiledIDs(raw, id)
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
	} {
		t.Run(extra, func(t *testing.T) {
			_, err := internalCompiledIDs([]byte(`{"model":{"configured_model_id":"`+id.String()+`"},`+extra+`}`), id)
			require.Error(t, err)
		})
	}
	_, err = internalCompiledIDs([]byte(`{"model":{"configured_model_id":"`+id.String()+`"}}`), uuid.New())
	require.ErrorContains(t, err, "does not match")
}
