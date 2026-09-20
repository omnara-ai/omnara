package toolcatalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestEveryAppOperationFlexibleAndFixedConfig(t *testing.T) {
	count := 0
	for _, id := range []string{appdefinition.Slack, appdefinition.GitHub, appdefinition.Discord} {
		definition, _ := appdefinition.Lookup(id)
		for _, operation := range definition.Tools {
			count++
			t.Run(id+"/"+operation, func(t *testing.T) {
				tool, ok := LookupAppTool(id, operation)
				require.True(t, ok)
				name := AppToolName("engineering-team", operation)
				destination := `{"channel_id":"C123","thread_ts":"111.222"}`
				switch id {
				case appdefinition.Discord:
					destination = `{"guild_id":"123","channel_id":"456","thread_id":"789"}`
				case appdefinition.GitHub:
					destination = `{"repository_id":123,"pull_request":42}`
				}
				action := `{}`
				switch operation {
				case AppOperationPostMessage:
					action = `{"text":"hello","follow_replies":true}`
					if id == appdefinition.Discord {
						action = `{"content":"hello","follow_replies":true}`
					}
				case AppOperationDiscussionComment:
					action = `{"body":"review"}`
				case AppOperationInlineComment:
					action = `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT"}`
				case AppOperationReply:
					action = `{"body":"reply","comment_id":123}`
				}
				fixed, err := tool.Prepare(name, []byte(destination))
				require.NoError(t, err)
				flexible, err := tool.Prepare(name, nil)
				require.NoError(t, err)
				require.NotContains(t, string(fixed.InputSchema), `"resource"`)
				require.NoError(t, jsonschema.Validate(fixed.InputSchema, []byte(action)))
				var fields, full map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(destination), &fields))
				require.NoError(t, json.Unmarshal([]byte(action), &full))
				for key, value := range fields {
					full[key] = value
					require.Contains(t, fixed.Description, strings.Trim(string(value), `"`))
				}
				arguments, err := json.Marshal(full)
				require.NoError(t, err)
				require.NoError(t, jsonschema.Validate(flexible.InputSchema, arguments))
				require.Error(t, jsonschema.Validate(fixed.InputSchema, arguments))
				a, err := tool.ResolveArgs([]byte(destination), []byte(action))
				require.NoError(t, err)
				b, err := tool.ResolveArgs(nil, arguments)
				require.NoError(t, err)
				require.Equal(t, a, b)
				_, err = tool.ResolveArgs([]byte(destination), arguments)
				require.Error(t, err)
				_, err = tool.CanonicalConfig([]byte(`{"unsupported":true}`))
				require.Error(t, err)
				if operation == AppOperationPostMessage {
					require.Equal(t, "thread_messages", tool.FollowListener)
					require.True(t, a.FollowReplies)
				}
			})
		}
	}
	require.Equal(t, 8, count)
	_, ok := LookupAppTool(appdefinition.Slack, AppOperationReply)
	require.False(t, ok)
}

func TestInlineCommentDependentArguments(t *testing.T) {
	tool, _ := LookupAppTool(appdefinition.GitHub, AppOperationInlineComment)
	config := json.RawMessage(`{"repository_id":123,"pull_request":42}`)
	base := `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT","start_line":3}`
	_, err := tool.ResolveArgs(config, []byte(base))
	require.Error(t, err)
	_, err = tool.ResolveArgs(config, []byte(base[:len(base)-1]+`,"start_side":"RIGHT"}`))
	require.NoError(t, err)
}

func TestAppToolPreparationKeepsCachedDefinitionsIsolated(t *testing.T) {
	t.Parallel()
	for _, config := range []string{`{}`, `{"channel_id":"C123"}`, `{"channel_id":"C456","thread_ts":"1.2"}`} {
		t.Run(config, func(t *testing.T) {
			t.Parallel()
			tool, ok := LookupAppTool(appdefinition.Slack, AppOperationPostMessage)
			require.True(t, ok)
			name := AppToolName("chat", AppOperationPostMessage)
			prepared, err := tool.Prepare(name, json.RawMessage(config))
			require.NoError(t, err)
			schema := string(prepared.InputSchema)
			// A caller owns prepared schema bytes and returned metadata values.
			prepared.InputSchema[0] = 'x'
			tool.Description = "changed by caller"
			fresh, ok := LookupAppTool(appdefinition.Slack, AppOperationPostMessage)
			require.True(t, ok)
			another, err := fresh.Prepare(name, json.RawMessage(config))
			require.NoError(t, err)
			require.Equal(t, schema, string(another.InputSchema))
			require.Equal(t, prepared.Description, another.Description)
			flexible, err := fresh.Prepare(name, nil)
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(flexible.InputSchema, []byte(`{"channel_id":"C789","text":"hello"}`)))
			require.Error(t, jsonschema.Validate(flexible.InputSchema, []byte(`{"channel_id":"C789"}`)))
		})
	}
}
