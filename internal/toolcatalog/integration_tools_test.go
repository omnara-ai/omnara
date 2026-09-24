package toolcatalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestConversationBoundIntegrationToolActionSchemas(t *testing.T) {
	for _, integrationType := range []integrationdefinition.Type{
		integrationdefinition.SlackThread, integrationdefinition.DiscordThread, integrationdefinition.GitHubPR,
	} {
		definition, ok := integrationdefinition.Lookup(integrationType)
		require.True(t, ok)
		for _, operation := range definition.Tools {
			t.Run(string(definition.IntegrationType)+"/"+operation, func(t *testing.T) {
				tool, ok := LookupIntegrationTool(definition.IntegrationType, operation)
				require.True(t, ok)
				require.Equal(t, IntegrationToolScopeConversation, tool.Scope)
				action := `{}`
				switch operation {
				case IntegrationOperationPostMessage:
					action = `{"text":"hello"}`
					if definition.IntegrationType == integrationdefinition.DiscordThread {
						action = `{"content":"hello"}`
					}
				case IntegrationOperationDiscussionComment:
					action = `{"body":"review"}`
				case IntegrationOperationInlineComment:
					action = `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT"}`
				case IntegrationOperationReply:
					action = `{"body":"reply","comment_id":9007199254740995}`
				}
				entry, err := tool.Prepare(IntegrationToolName("engineering-team", operation))
				require.NoError(t, err)
				require.NoError(t, jsonschema.Validate(entry.InputSchema, []byte(action)))
				var schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				}
				require.NoError(t, json.Unmarshal(entry.InputSchema, &schema))
				for _, field := range []string{
					"channel_id", "thread_ts", "guild_id", "thread_id", "repository_id", "pull_request", "follow_replies",
				} {
					require.NotContains(t, schema.Properties, field, "model cannot select a conversation")
				}
				require.Error(t, jsonschema.Validate(entry.InputSchema, []byte(`null`)))
				if operation != IntegrationOperationRead {
					require.Error(t, jsonschema.Validate(entry.InputSchema, []byte(`{}`)), "action fields are required")
				}
			})
		}
	}
}

func TestIntegrationToolScopeDeclaration(t *testing.T) {
	tool := IntegrationToolDefinition{
		IntegrationType: integrationdefinition.SlackThread,
		Operation:       "lookup",
		Description:     "Look up a user.",
	}
	for _, scope := range []IntegrationToolScope{
		IntegrationToolScopeIntegration,
		IntegrationToolScopeConversation,
		"",
		"unknown",
	} {
		t.Run(string(scope), func(t *testing.T) {
			tool.Scope = scope
			_, err := tool.Prepare(IntegrationToolName("support", "lookup"))
			if scope == IntegrationToolScopeIntegration || scope == IntegrationToolScopeConversation {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, "explicit scope")
			}
		})
	}
}

func TestDeclaredIntegrationToolsHaveValidSchemas(t *testing.T) {
	for _, integration := range integrationdefinition.All() {
		for _, operation := range integration.Tools {
			t.Run(string(integration.IntegrationType)+"/"+operation, func(t *testing.T) {
				tool, found := LookupIntegrationTool(integration.IntegrationType, operation)
				require.True(t, found)
				_, err := tool.Prepare(IntegrationToolName("example", operation))
				require.NoError(t, err)
			})
		}
	}
}

func TestInlineCommentDependentArguments(t *testing.T) {
	tool, _ := LookupIntegrationTool(integrationdefinition.GitHubPR, IntegrationOperationInlineComment)
	entry, err := tool.Prepare(IntegrationToolName("reviews", IntegrationOperationInlineComment))
	require.NoError(t, err)
	base := `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT","start_line":3}`
	require.Error(t, jsonschema.Validate(entry.InputSchema, []byte(base)))
	require.NoError(t, jsonschema.Validate(entry.InputSchema, []byte(base[:len(base)-1]+`,"start_side":"RIGHT"}`)))
}

func TestIntegrationToolPreparationKeepsCachedDefinitionsIsolated(t *testing.T) {
	t.Parallel()
	for _, integration := range []string{"chat", "support", "engineering"} {
		t.Run(integration, func(t *testing.T) {
			t.Parallel()
			tool, _ := LookupIntegrationTool(integrationdefinition.SlackThread, IntegrationOperationPostMessage)
			name := IntegrationToolName(integration, IntegrationOperationPostMessage)
			prepared, err := tool.Prepare(name)
			require.NoError(t, err)
			schema := string(prepared.InputSchema)
			prepared.InputSchema[0] = 'x'
			tool.Description = "changed by caller"
			fresh, _ := LookupIntegrationTool(integrationdefinition.SlackThread, IntegrationOperationPostMessage)
			another, err := fresh.Prepare(name)
			require.NoError(t, err)
			require.Equal(t, schema, string(another.InputSchema))
			require.Equal(t, prepared.Description, another.Description)
		})
	}
}

func TestDiscordMessageContentLengthSchema(t *testing.T) {
	tool, ok := LookupIntegrationTool(integrationdefinition.DiscordThread, IntegrationOperationPostMessage)
	require.True(t, ok)
	entry, err := tool.Prepare(IntegrationToolName("chat", IntegrationOperationPostMessage))
	require.NoError(t, err)
	for _, size := range []int{2000, 2001} {
		raw, err := json.Marshal(map[string]string{"content": strings.Repeat("🙂", size)})
		require.NoError(t, err)
		err = jsonschema.Validate(entry.InputSchema, raw)
		if size == 2000 {
			require.NoError(t, err)
		} else {
			require.Error(t, err)
		}
	}
}
