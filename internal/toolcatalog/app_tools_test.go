package toolcatalog

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestProviderActionSchemas(t *testing.T) {
	catalog, err := Default()
	require.NoError(t, err)
	for _, test := range []struct{ name, valid, invalid string }{
		{ToolNameSlackRead, `{"limit":50}`, `{"limit":101}`},
		{
			ToolNameSlackPostMessage,
			`{"text":"hello","thread_ts":"111.222","follow_replies":true}`,
			`{"content":"wrong provider"}`,
		},
		{ToolNameGitHubRead, `{}`, `{"pull_request":999}`},
		{ToolNameGitHubDiscussionComment, `{"body":"review"}`, `{"text":"wrong provider"}`},
		{
			ToolNameGitHubInlineComment,
			`{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT",
			"start_line":3,"start_side":"RIGHT"}`,
			`{"body":"review","commit_id":"abc","path":"a.go","line":7,"side":"RIGHT","start_line":3}`,
		},
		{ToolNameGitHubReply, `{"comment_id":123,"body":"reply"}`, `{"comment_id":"invented","body":"reply"}`},
		{ToolNameDiscordRead, `{"before":"123","limit":10}`, `{"limit":0}`},
		{ToolNameDiscordPostMessage, `{"content":"hello"}`, `{"text":"wrong provider"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			entry, ok := catalog.Lookup(test.name)
			require.True(t, ok)
			require.True(t, entry.Implicit)
			original := string(entry.InputSchema)
			keys := []string{"second", "first"}
			many, err := entry.WithAppResources(keys)
			require.NoError(t, err)
			require.Equal(t, []string{"second", "first"}, keys)
			one, err := entry.WithAppResources([]string{"only"})
			require.NoError(t, err)
			validator, err := jsonschema.Compile(one.InputSchema)
			require.NoError(t, err)
			require.NoError(t, validator.Validate([]byte(test.valid)))
			require.Error(t, validator.Validate([]byte(test.invalid)))
			multi, err := jsonschema.Compile(many.InputSchema)
			require.NoError(t, err)
			require.Error(t, multi.Validate([]byte(test.valid)))
			var args map[string]any
			require.NoError(t, json.Unmarshal([]byte(test.valid), &args))
			args["resource"] = "first"
			raw, err := json.Marshal(args)
			require.NoError(t, err)
			require.NoError(t, multi.Validate(raw))
			require.Error(t, validator.Validate(raw))
			unchanged, _ := catalog.Lookup(test.name)
			require.Equal(t, original, string(unchanged.InputSchema))
		})
	}
	ordinary, _ := catalog.Lookup(ToolNameWebFetch)
	_, err = ordinary.WithAppResources([]string{"no-injection"})
	require.Error(t, err)
	_, ok := catalog.Lookup("send_integration_message")
	require.False(t, ok)
}
