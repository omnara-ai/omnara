package toolcatalog

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestEveryAppOperationWithContextAndExplicitDestination(t *testing.T) {
	for _, id := range []string{appdefinition.Slack, appdefinition.GitHub, appdefinition.Discord} {
		definition, _ := appdefinition.Lookup(id)
		for _, operation := range definition.Tools {
			t.Run(id+"/"+operation, func(t *testing.T) {
				tool, ok := LookupAppTool(id, operation)
				require.True(t, ok)
				destination := `{"channel_id":"C123","thread_ts":"111.222"}`
				switch id {
				case appdefinition.Discord:
					destination = `{"guild_id":"123","channel_id":"456","thread_id":"789"}`
				case appdefinition.GitHub:
					destination = `{"repository_id":9007199254740993,"pull_request":42}`
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
					action = `{"body":"reply","comment_id":9007199254740995}`
				}
				entry, err := tool.Prepare(AppToolName("engineering-team", operation))
				require.NoError(t, err)
				require.NoError(t, jsonschema.Validate(entry.InputSchema, []byte(action)))
				context, err := appdefinition.ResolveDestination(definition.Provider, []byte(destination))
				require.NoError(t, err)
				resolved, err := tool.ResolveArgs([]byte(action), &context)
				require.NoError(t, err)
				require.Equal(t, context, resolved.Destination)
				require.JSONEq(t, action, string(resolved.Arguments), "address stays separate from action, without rounding IDs")
				if operation == AppOperationReply {
					require.Contains(t, string(resolved.Arguments), `"comment_id":9007199254740995`)
				}
				_, err = tool.ResolveArgs([]byte(action), nil)
				require.Error(t, err, "unbound execution requires a concrete destination")

				var fields, full map[string]json.RawMessage
				require.NoError(t, json.Unmarshal([]byte(destination), &fields))
				require.NoError(t, json.Unmarshal([]byte(action), &full))
				for key, value := range fields {
					full[key] = value
				}
				arguments, err := json.Marshal(full)
				require.NoError(t, err)
				require.NoError(t, jsonschema.Validate(entry.InputSchema, arguments))
				for _, ctx := range []*appdefinition.Scope{nil, &context} {
					explicit, err := tool.ResolveArgs(arguments, ctx)
					require.NoError(t, err)
					require.Equal(t, resolved, explicit)
				}
				if operation == AppOperationPostMessage {
					require.Equal(t, "thread_messages", tool.FollowSubscription)
					require.True(t, resolved.FollowReplies)
					require.Contains(t, definition.Subscriptions, tool.FollowSubscription)
				} else {
					require.Empty(t, tool.FollowSubscription)
					require.False(t, resolved.FollowReplies)
				}
			})
		}
	}
}

func TestAppToolContextRejectsDestinationChanges(t *testing.T) {
	for _, test := range []struct {
		definition, kind, ref string
		matching, mismatching []string
	}{
		{appdefinition.Slack, "thread", "C123:1.2",
			[]string{`{}`, `{"channel_id":"C123"}`, `{"thread_ts":"1.2"}`},
			[]string{`{"channel_id":"C456"}`, `{"thread_ts":"1.3"}`, `{"thread_ts":null}`}},
		{appdefinition.Slack, "channel", "C123",
			[]string{`{}`, `{"channel_id":"C123"}`}, []string{`{"channel_id":"C456","thread_ts":"1.2"}`}},
		{appdefinition.GitHub, "pull_request", "9007199254740993#42",
			[]string{`{}`, `{"repository_id":9007199254740993}`, `{"pull_request":42}`},
			[]string{`{"repository_id":9007199254740992}`, `{"pull_request":43}`}},
		{appdefinition.Discord, "thread", "456:789",
			[]string{`{}`, `{"channel_id":"456"}`, `{"thread_id":"789"}`, `{"guild_id":"123"}`},
			[]string{`{"channel_id":"457"}`, `{"thread_id":"790"}`, `{"guild_id":"invalid"}`}},
		{appdefinition.Discord, "channel", "456",
			[]string{`{}`, `{"channel_id":"456"}`}, []string{`{"channel_id":"457","thread_id":"789"}`}},
	} {
		t.Run(test.definition+test.ref, func(t *testing.T) {
			tool, _ := LookupAppTool(test.definition, AppOperationRead)
			context, err := appdefinition.ParseConversation(tool.Provider, test.kind, test.ref)
			require.NoError(t, err)
			before, err := context.ConversationJSON()
			require.NoError(t, err)
			for _, raw := range test.matching {
				resolved, err := tool.ResolveArgs([]byte(raw), &context)
				require.NoError(t, err, raw)
				kind, ref, err := resolved.Destination.Conversation()
				require.NoError(t, err)
				require.Equal(t, test.kind, kind)
				require.Equal(t, test.ref, ref)
				require.JSONEq(t, `{}`, string(resolved.Arguments))
			}
			for _, raw := range test.mismatching {
				_, err := tool.ResolveArgs([]byte(raw), &context)
				require.Error(t, err, raw)
			}
			after, err := context.ConversationJSON()
			require.NoError(t, err)
			require.Equal(t, before, after, "resolution never mutates context")
		})
	}
}

// Migration preserves channel-only and DM sending scopes: child threads remain
// reachable, but narrowing a call to a thread does not mutate the stored context.
func TestAppToolChannelContextPreservesLegacyChildThreads(t *testing.T) {
	for _, test := range []struct{ definition, kind, ref, args, wantRef, escape string }{
		{appdefinition.Slack, "channel", "C123", `{"text":"hello","thread_ts":"1.2"}`, "C123:1.2",
			`{"text":"hello","thread_ts":"3.4"}`},
		{appdefinition.Slack, "dm", "D123", `{"text":"hello","thread_ts":"1.2"}`, "D123:1.2",
			`{"text":"hello","thread_ts":"3.4"}`},
		{appdefinition.Discord, "channel", "456", `{"content":"hello","thread_id":"789"}`, "456:789",
			`{"content":"hello","thread_id":"790"}`},
	} {
		t.Run(test.definition+"/"+test.kind, func(t *testing.T) {
			tool, _ := LookupAppTool(test.definition, AppOperationPostMessage)
			context, err := appdefinition.ParseConversation(tool.Provider, test.kind, test.ref)
			require.NoError(t, err)
			child, err := tool.ResolveArgs([]byte(test.args), &context)
			require.NoError(t, err, "the original channel scope includes child threads")
			kind, ref, err := child.Destination.Conversation()
			require.NoError(t, err)
			require.Equal(t, "thread", kind)
			require.Equal(t, test.wantRef, ref)
			_, err = tool.ResolveArgs([]byte(test.escape), &child.Destination)
			require.ErrorContains(t, err, "does not match tool context", "a thread context cannot escape to a sibling")
			omitted, err := tool.ResolveArgs(child.Arguments, &child.Destination)
			require.NoError(t, err)
			require.Equal(t, child.Destination, omitted.Destination, "omission cannot widen a thread context")
			kind, ref, err = context.Conversation()
			require.NoError(t, err)
			require.Equal(t, test.kind, kind)
			require.Equal(t, test.ref, ref, "child arguments never narrow the immutable parent context")
		})
	}
}

func TestDiscordContextGuildMetadata(t *testing.T) {
	tool, _ := LookupAppTool(appdefinition.Discord, AppOperationPostMessage)
	context := appdefinition.Scope{Discord: &appdefinition.DiscordScope{ChannelID: "456", ThreadID: "789"}}
	resolved, err := tool.ResolveArgs([]byte(`{"content":"hello","guild_id":"123"}`), &context)
	require.NoError(t, err)
	require.Equal(t, "123", resolved.Destination.Discord.GuildID, "supplied guild survives for live provider verification")
	require.Empty(t, context.Discord.GuildID)
	context.Discord.GuildID = "123"
	resolved, err = tool.ResolveArgs([]byte(`{"content":"hello"}`), &context)
	require.NoError(t, err)
	require.Equal(t, context, resolved.Destination)
	_, err = tool.ResolveArgs([]byte(`{"content":"hello","guild_id":"124"}`), &context)
	require.ErrorContains(t, err, "does not match tool context")
}

func TestAppToolRejectsInvalidContextAndArguments(t *testing.T) {
	tool, _ := LookupAppTool(appdefinition.Slack, AppOperationPostMessage)
	for _, context := range []appdefinition.Scope{
		{}, {Slack: &appdefinition.SlackScope{ThreadTS: "1.2"}},
		{Discord: &appdefinition.DiscordScope{ChannelID: "123"}},
	} {
		_, err := tool.ResolveArgs([]byte(`{"text":"hello","channel_id":"C123"}`), &context)
		require.Error(t, err)
	}
	for _, raw := range []string{
		`{"channel_id":"C123"}`, `{"text":"hello","channel_id":"C123","unexpected":true}`,
		`{"text":"hello","thread_ts":"1.2"}`, `null`,
	} {
		_, err := tool.ResolveArgs([]byte(raw), nil)
		require.Error(t, err)
	}
}

func TestInlineCommentDependentArguments(t *testing.T) {
	tool, _ := LookupAppTool(appdefinition.GitHub, AppOperationInlineComment)
	context := appdefinition.Scope{GitHub: &appdefinition.GitHubScope{RepositoryID: 123, PullRequest: 42}}
	base := `{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT","start_line":3}`
	_, err := tool.ResolveArgs([]byte(base), &context)
	require.Error(t, err)
	_, err = tool.ResolveArgs([]byte(base[:len(base)-1]+`,"start_side":"RIGHT"}`), &context)
	require.NoError(t, err)
}

func TestAppToolPreparationKeepsCachedDefinitionsIsolated(t *testing.T) {
	t.Parallel()
	for _, app := range []string{"chat", "support", "engineering"} {
		t.Run(app, func(t *testing.T) {
			t.Parallel()
			tool, _ := LookupAppTool(appdefinition.Slack, AppOperationPostMessage)
			name := AppToolName(app, AppOperationPostMessage)
			prepared, err := tool.Prepare(name)
			require.NoError(t, err)
			schema := string(prepared.InputSchema)
			prepared.InputSchema[0] = 'x'
			tool.Description = "changed by caller"
			fresh, _ := LookupAppTool(appdefinition.Slack, AppOperationPostMessage)
			another, err := fresh.Prepare(name)
			require.NoError(t, err)
			require.Equal(t, schema, string(another.InputSchema))
			require.Equal(t, prepared.Description, another.Description)
		})
	}
}
