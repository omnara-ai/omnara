package migrations

import (
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/stretchr/testify/require"
)

func TestSlackAppConfigRewritePreservesPolicyAndNullableSource(t *testing.T) {
	compiled := []byte(
		`{"instruction":"Keep send_integration_message as historical text.","tools":{"send_integration_message":{"enabled":false,"permission":{"mode":"always_deny","parameters":{}}}}}`,
	)
	hash, err := explicitDefaultToolsConfigHash(compiled)
	require.NoError(t, err)
	for _, format := range []string{"", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			config := appCutoverConfig{definition: compiled, compiled: compiled, hash: hash}
			if format != "" {
				source := string(compiled)
				if format == "yaml" {
					source = `instruction: Keep send_integration_message as historical text.
tools:
  send_integration_message: &policy
    enabled: false
    permission: {mode: always_deny, parameters: {}}
  other_tool: *policy
`
				}
				config.source = sql.NullString{String: source, Valid: true}
				config.format = sql.NullString{String: format, Valid: true}
				config.sourceHash = sql.NullString{String: hashBytes([]byte(source)), Valid: true}
			}
			updated, changed, err := rewriteSlackAppConfig(config)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, config.source.Valid, updated.source.Valid)
			var actual agentconfig.Compiled
			require.NoError(t, json.Unmarshal(updated.compiled, &actual))
			require.False(t, actual.Tools["slack_post_message"].Enabled)
			require.Equal(t, "always_deny", actual.Tools["slack_post_message"].Permission.Mode)
			require.Equal(t, "Keep send_integration_message as historical text.", actual.Instruction)
			require.Nil(t, actual.AppResources)
			if updated.source.Valid {
				require.Equal(t, hashBytes([]byte(updated.source.String)), updated.sourceHash.String)
			}
			again, changed, err := rewriteSlackAppConfig(updated)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, updated, again)
		})
	}
}

func TestSlackSendingSuccessorScopesOnlyThatAgentsTargets(t *testing.T) {
	connection, firstTarget, secondTarget := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, policy := range []string{
		`{}`, `{"slack_post_message":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}`,
		`{"slack_post_message":{"enabled":true,"permission":{"mode":"always_ask","parameters":{}}}}`,
	} {
		t.Run(policy, func(t *testing.T) {
			original := []byte(`{"instruction":"Review","tools":` + policy + `}`)
			first, err := slackSendingSuccessor(
				original,
				[]slackCutoverTarget{{id: firstTarget, connectionID: connection, kind: "thread", ref: "C123:111.222"}},
			)
			require.NoError(t, err)
			second, err := slackSendingSuccessor(
				original,
				[]slackCutoverTarget{{id: secondTarget, connectionID: connection, kind: "dm", ref: "D456"}},
			)
			require.NoError(t, err)
			for _, raw := range [][]byte{first, second} {
				hash, err := explicitDefaultToolsConfigHash(raw)
				require.NoError(t, err)
				contract, err := agentconfig.RuntimeContractFromCompiled(raw, "", hash)
				require.NoError(t, err)
				require.Len(t, contract.AppResources, 1)
				for _, resource := range contract.AppResources {
					require.Nil(t, resource.Listener)
					require.Nil(t, resource.Follow)
					require.Nil(t, resource.InteractionHandler)
					require.Equal(t, []string{"slack_post_message"}, resource.Tools)
				}
			}
			require.NotContains(t, string(first), "D456")
			require.NotContains(t, string(second), "C123")
			require.Equal(t, `{"instruction":"Review","tools":`+policy+`}`, string(original))
		})
	}
}

func TestSlackAppMigrationRejectsConflictingPolicy(t *testing.T) {
	_, _, err := renameAppToolsJSON(
		[]byte(`{"tools":{"send_integration_message":{"enabled":false},"slack_post_message":{"enabled":true}}}`),
	)
	require.ErrorContains(t, err, "different settings")
}

func TestSlackAppYAMLRewritePreservesCommentsAndFormatting(t *testing.T) {
	source := `# profile note
instruction: |-
  Preserve this paragraph.

  And this one.
tools:
  # Explicitly disabled
  send_integration_message: {enabled: false} # policy
`
	updated, changed, err := renameAppToolsYAML([]byte(source))
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(updated), "# profile note")
	require.Contains(t, string(updated), "# Explicitly disabled")
	require.Contains(t, string(updated), "slack_post_message: {enabled: false} # policy")
	require.Contains(t, string(updated), "instruction: |-\n  Preserve this paragraph.\n\n  And this one.")
}

func TestSlackSendingSuccessorRejectsInvalidLegacyAddress(t *testing.T) {
	for _, target := range []slackCutoverTarget{
		{kind: "thread", ref: "C123:invalid"}, {kind: "thread", ref: "wrong:111.222"},
		{kind: "channel", ref: "*"}, {kind: "dm", ref: ""},
	} {
		target.id, target.connectionID = uuid.NewString(), uuid.NewString()
		_, err := slackSendingSuccessor([]byte(`{"tools":{}}`), []slackCutoverTarget{target})
		require.ErrorContains(t, err, "invalid Slack target")
	}
}
