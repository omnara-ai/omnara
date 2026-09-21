package toolcatalog

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestConversationBoundAppToolActionSchemas(t *testing.T) {
	for _, appType := range []appdefinition.Type{
		appdefinition.SlackThread, appdefinition.DiscordThread, appdefinition.GitHubPR,
	} {
		definition, ok := appdefinition.Lookup(appType)
		require.True(t, ok)
		for _, operation := range definition.Tools {
			t.Run(string(definition.AppType)+"/"+operation, func(t *testing.T) {
				tool, ok := LookupAppTool(definition.AppType, operation)
				require.True(t, ok)
				action := `{}`
				switch operation {
				case AppOperationPostMessage:
					action = `{"text":"hello"}`
					if definition.AppType == appdefinition.DiscordThread {
						action = `{"content":"hello"}`
					}
				case AppOperationDiscussionComment:
					action = `{"body":"review"}`
				case AppOperationInlineComment:
					action = `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT"}`
				case AppOperationReply:
					action = `{"body":"reply","comment_id":9007199254740995}`
				}
				entry, err := tool.Prepare(AppToolName("engineering-team", operation))
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
				if operation != AppOperationRead {
					require.Error(t, jsonschema.Validate(entry.InputSchema, []byte(`{}`)), "action fields are required")
				}
			})
		}
	}
}

func TestInlineCommentDependentArguments(t *testing.T) {
	tool, _ := LookupAppTool(appdefinition.GitHubPR, AppOperationInlineComment)
	entry, err := tool.Prepare(AppToolName("reviews", AppOperationInlineComment))
	require.NoError(t, err)
	base := `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT","start_line":3}`
	require.Error(t, jsonschema.Validate(entry.InputSchema, []byte(base)))
	require.NoError(t, jsonschema.Validate(entry.InputSchema, []byte(base[:len(base)-1]+`,"start_side":"RIGHT"}`)))
}

func TestAppToolPreparationKeepsCachedDefinitionsIsolated(t *testing.T) {
	t.Parallel()
	for _, app := range []string{"chat", "support", "engineering"} {
		t.Run(app, func(t *testing.T) {
			t.Parallel()
			tool, _ := LookupAppTool(appdefinition.SlackThread, AppOperationPostMessage)
			name := AppToolName(app, AppOperationPostMessage)
			prepared, err := tool.Prepare(name)
			require.NoError(t, err)
			schema := string(prepared.InputSchema)
			prepared.InputSchema[0] = 'x'
			tool.Description = "changed by caller"
			fresh, _ := LookupAppTool(appdefinition.SlackThread, AppOperationPostMessage)
			another, err := fresh.Prepare(name)
			require.NoError(t, err)
			require.Equal(t, schema, string(another.InputSchema))
			require.Equal(t, prepared.Description, another.Description)
		})
	}
}
