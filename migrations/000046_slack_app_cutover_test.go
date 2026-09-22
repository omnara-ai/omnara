package migrations

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/stretchr/testify/require"
)

func TestSlackAppConfigRewritePreservesPolicyAndNullableSource(t *testing.T) {
	compiled := []byte(
		`{"instruction":"Keep send_integration_message as historical text.","tools":{"send_integration_message":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}}`,
	)
	apps := []slackCutoverApp{{id: uuid.NewString(), name: "slack"}, {id: uuid.NewString(), name: "slack-2"}}
	hash, err := explicitDefaultToolsConfigHash(compiled)
	require.NoError(t, err)
	for _, format := range []string{"", "json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			config := appCutoverConfig{compiled: compiled, hash: hash}
			if format != "" {
				source := string(compiled)
				if format == "yaml" {
					source = `instruction: Keep send_integration_message as historical text.
tools:
  send_integration_message: &policy
    enabled: false
    permission: {mode: always_allow, parameters: {}}
  other_tool: *policy
`
				}
				config.source = sql.NullString{String: source, Valid: true}
				config.format = sql.NullString{String: format, Valid: true}
				config.sourceHash = sql.NullString{String: hashBytes([]byte(source)), Valid: true}
			}
			updated, changed, err := rewriteSlackAppConfig(config, apps)
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, config.source.Valid, updated.source.Valid)
			var actual agentconfig.Compiled
			require.NoError(t, json.Unmarshal(updated.compiled, &actual))
			for _, app := range apps {
				tool, exists := actual.Tools[app.toolName()]
				require.True(t, exists)
				require.False(t, tool.Enabled)
				require.Equal(t, "always_allow", tool.Permission.Mode)
				require.Equal(t, app.id, tool.AppID.String())
			}
			_, err = agentconfig.RuntimeContractFromCompiled(updated.compiled, updated.hash)
			require.NoError(t, err)
			require.Equal(t, "Keep send_integration_message as historical text.", actual.Instruction)
			require.Empty(t, actual.InteractionHandlers)
			if updated.source.Valid {
				require.NotContains(t, updated.source.String, "app_id")
				require.Equal(t, hashBytes([]byte(updated.source.String)), updated.sourceHash.String)
			}
			again, changed, err := rewriteSlackAppConfig(updated, apps)
			require.NoError(t, err)
			require.False(t, changed)
			require.Equal(t, updated, again)
		})
	}
}

func TestSlackSendingSuccessorPinsAppsAndPreservesPolicies(t *testing.T) {
	appID, firstTarget, secondTarget := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for _, policy := range []string{
		`{}`, `{"send_integration_message":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}`,
		`{"send_integration_message":{"enabled":true,"deferred":true,"permission":{"mode":"always_allow","parameters":{}}}}`,
	} {
		t.Run(policy, func(t *testing.T) {
			original := []byte(`{"instruction":"Review","tools":` + policy + `}`)
			first, err := slackSendingSuccessor(
				original,
				[]slackCutoverTarget{{id: firstTarget, appID: appID, appName: "slack", kind: "thread", ref: "C123:111.222"}},
			)
			require.NoError(t, err)
			second, err := slackSendingSuccessor(
				original,
				[]slackCutoverTarget{{id: secondTarget, appID: appID, appName: "slack", kind: "dm", ref: "D456"}},
			)
			require.NoError(t, err)
			for _, raw := range [][]byte{first, second} {
				hash, err := explicitDefaultToolsConfigHash(raw)
				require.NoError(t, err)
				contract, err := agentconfig.RuntimeContractFromCompiled(raw, hash)
				require.NoError(t, err)
				require.Len(t, contract.AppTools, 1)
				require.Empty(t, contract.InteractionHandlers)
				tool := contract.AppTools["app__slack__post_message"]
				require.Equal(t, "always_allow", tool.Permission.Mode)
				require.Equal(t, appID, tool.AppID.String())
				if policy == `{}` {
					require.True(t, tool.Enabled)
				} else {
					var original map[string]agentconfig.ToolCompiled
					require.NoError(t, json.Unmarshal([]byte(policy), &original))
					require.Equal(t, original["send_integration_message"].Enabled, tool.Enabled)
					require.Equal(t, original["send_integration_message"].Deferred, tool.Deferred)
				}
			}
			require.JSONEq(t, string(first), string(second), "destinations now belong to immutable target contexts")
			require.Equal(t, `{"instruction":"Review","tools":`+policy+`}`, string(original))
		})
	}
}

func TestSlackAppMigrationRejectsUnmappablePolicies(t *testing.T) {
	for _, policy := range []string{
		`{"enabled":true,"permission":{"mode":"always_ask","parameters":{}}}`,
		`{"enabled":false,"permission":{"mode":"always_deny","parameters":{}}}`,
		`{"enabled":true,"permission":{"mode":"always_allow","parameters":{"channel":"C123"}}}`,
		`{"enabled":"false"}`, `{"type":"custom"}`, `{"type":null}`, `{"deferred":null}`,
		`{"config":{"channel_id":"C123"}}`, `null`,
	} {
		t.Run(policy, func(t *testing.T) {
			_, _, err := rewriteSlackToolsJSON(
				[]byte(
					`{"tools":{"send_integration_message":`+policy+`}}`,
				),
				[]slackCutoverApp{{id: uuid.NewString(), name: "slack"}},
				true,
			)
			require.Error(t, err)
		})
	}
	_, _, err := rewriteSlackToolsJSON([]byte(`{"tools":{"send_integration_message":{"enabled":false}}}`), nil, true)
	require.ErrorContains(t, err, "no known Slack app")
}

func TestSlackAppMigrationDropsEnabledHistoricalSending(t *testing.T) {
	for _, policy := range []string{`{}`, `{"enabled":true,"permission":{"mode":"always_allow","parameters":{}}}`} {
		for _, compiled := range []bool{false, true} {
			raw, changed, err := rewriteSlackToolsJSON(
				[]byte(
					`{"tools":{"send_integration_message":`+policy+`}}`,
				),
				[]slackCutoverApp{{id: uuid.NewString(), name: "slack"}},
				compiled,
			)
			require.NoError(t, err)
			require.True(t, changed)
			require.JSONEq(t, `{"tools":{}}`, string(raw))
		}
	}
}

func TestSlackAppMigrationPreservesHandlerSelectionPolicy(t *testing.T) {
	raw, _, err := rewriteSlackToolsJSON(
		[]byte(
			`{"tools":{"set_integration_target":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}}`,
		),
		nil,
		true,
	)
	require.NoError(t, err)
	require.JSONEq(
		t,
		`{"tools":{"set_interaction_handler":{"enabled":false,"permission":{"mode":"always_allow","parameters":{}}}}}`,
		string(
			raw,
		),
	)
	_, _, err = rewriteSlackToolsJSON(
		[]byte(
			`{"tools":{"set_integration_target":{"enabled":false},"set_interaction_handler":{"enabled":true}}}`,
		),
		nil,
		true,
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
	updated, changed, err := rewriteSlackToolsYAML(
		[]byte(
			source,
		),
		[]slackCutoverApp{{id: uuid.NewString(), name: "slack"}},
	)
	require.NoError(t, err)
	require.True(t, changed)
	require.Contains(t, string(updated), "# profile note")
	require.Contains(t, string(updated), "# Explicitly disabled")
	require.Contains(t, string(updated), "app__slack__post_message: {enabled: false} # policy")
	require.Contains(t, string(updated), "instruction: |-\n  Preserve this paragraph.\n\n  And this one.")
}

func TestSlackSendingSuccessorRejectsInvalidLegacyAddress(t *testing.T) {
	for _, target := range []slackCutoverTarget{
		{kind: "thread", ref: "C123:invalid"}, {kind: "thread", ref: "wrong:111.222"},
		{kind: "channel", ref: "*"}, {kind: "dm", ref: ""},
	} {
		target.id, target.appID, target.appName = uuid.NewString(), uuid.NewString(), "slack"
		_, err := slackSendingSuccessor([]byte(`{"tools":{}}`), []slackCutoverTarget{target})
		require.ErrorContains(t, err, "invalid Slack target")
	}
}

func TestSlackSendingSuccessorRejectsSeveralTargetsForOneApp(t *testing.T) {
	appID := uuid.NewString()
	targets := []slackCutoverTarget{
		{id: uuid.NewString(), appID: appID, appName: "slack", kind: "channel", ref: "C123"},
		{id: uuid.NewString(), appID: appID, appName: "slack", kind: "channel", ref: "C456"},
	}
	_, err := slackSendingSuccessor([]byte(`{"tools":{}}`), targets)
	require.ErrorContains(t, err, "belong to one Slack app")
	for _, target := range targets {
		require.ErrorContains(t, err, target.id)
	}
}

func TestSlackAppMigrationOmitsDeletedAppPoliciesFromSourceAndCanonicalConfig(t *testing.T) {
	deleted := slackCutoverApp{id: uuid.NewString(), name: "slack-2", deleted: true}
	live := slackCutoverApp{id: uuid.NewString(), name: "slack"}
	compiled := []byte(
		`{"instruction":"Review","tools":{"send_integration_message":{"enabled":false,"deferred":true,"permission":{"mode":"always_allow","parameters":{}}}}}`,
	)
	for _, onlyDeleted := range []bool{false, true} {
		for _, format := range []string{"", "json", "yaml", "yaml_alias"} {
			t.Run(fmt.Sprintf("only_deleted=%t/%s", onlyDeleted, format), func(t *testing.T) {
				apps := []slackCutoverApp{deleted}
				if !onlyDeleted {
					apps = append(apps, live)
				}
				hash, err := explicitDefaultToolsConfigHash(compiled)
				require.NoError(t, err)
				config := appCutoverConfig{compiled: compiled, hash: hash}
				if format != "" {
					source := strings.Replace(
						string(
							compiled,
						),
						`"instruction":"Review"`,
						`"instruction":"Review","model":{"provider_config":"openai-prod","name":"test"}`,
						1,
					)
					if strings.HasPrefix(format, "yaml") {
						source = "# Keep profile note\ninstruction: Review\n" +
							"model: {provider_config: openai-prod, name: test}\ntools:\n" +
							"  send_integration_message: {enabled: false, deferred: true," +
							" permission: {mode: always_allow, parameters: {}}}\n"
						if format == "yaml_alias" {
							source += "  read_file: &other {enabled: false}\n  write_file: *other\n"
						}
					}
					config.source = sql.NullString{String: source, Valid: true}
					config.format = sql.NullString{String: strings.TrimSuffix(format, "_alias"), Valid: true}
					config.sourceHash = sql.NullString{String: hashBytes([]byte(source)), Valid: true}
				}
				updated, changed, err := rewriteSlackAppConfig(config, apps)
				require.NoError(t, err)
				require.True(t, changed)
				var result agentconfig.Compiled
				require.NoError(t, json.Unmarshal(updated.compiled, &result))
				require.NotContains(t, result.Tools, deleted.toolName())
				require.NotContains(t, result.Tools, "send_integration_message")
				if onlyDeleted {
					require.Empty(t, result.Tools)
				} else {
					tool, exists := result.Tools[live.toolName()]
					require.True(t, exists)
					require.False(t, tool.Enabled)
					require.True(t, tool.Deferred)
					require.Equal(t, live.id, tool.AppID.String())
				}
				_, err = agentconfig.RuntimeContractFromCompiled(updated.compiled, updated.hash)
				require.NoError(t, err)
				if updated.source.Valid {
					require.NotContains(t, updated.source.String, deleted.toolName())
					require.NotContains(t, updated.source.String, "send_integration_message")
					require.NotContains(t, updated.source.String, "app_id")
					if !onlyDeleted {
						require.Contains(t, updated.source.String, live.toolName())
					}
					require.Equal(t, hashBytes([]byte(updated.source.String)), updated.sourceHash.String)
					_, err := agentconfig.ParseSource(agentconfig.SourceFormat(config.format.String), []byte(updated.source.String))
					require.NoError(t, err, "removing the last policy must leave valid JSON/YAML tools")
				}
				again, changed, err := rewriteSlackAppConfig(updated, apps)
				require.NoError(t, err)
				require.False(t, changed)
				require.Equal(t, updated, again)
			})
		}
	}
}
