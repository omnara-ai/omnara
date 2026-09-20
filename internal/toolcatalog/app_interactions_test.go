package toolcatalog

import (
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestInteractionHandlerToolSchemas(t *testing.T) {
	catalog, err := Default()
	require.NoError(t, err)
	list, ok := catalog.Lookup(ToolNameListInteractionHandlers)
	require.True(t, ok)
	set, ok := catalog.Lookup(ToolNameSetInteractionHandler)
	require.True(t, ok)
	for _, raw := range []string{`{}`, `{"cursor":"cursor","limit":100}`} {
		require.NoError(t, jsonschema.Validate(list.InputSchema, []byte(raw)))
	}
	for _, raw := range []string{`{"limit":101}`, `{"limit":0}`, `{"cursor":""}`} {
		require.Error(t, jsonschema.Validate(list.InputSchema, []byte(raw)))
	}
	for _, raw := range []string{`{"handler":null,"args":{}}`, `{"handler":"engineering","args":{"channel_id":"C123"}}`} {
		require.NoError(t, jsonschema.Validate(set.InputSchema, []byte(raw)))
	}
	for _, raw := range []string{`{}`, `{"handler":null}`, `{"handler":"engineering","args":[]}`, `{"destination":null}`} {
		require.Error(t, jsonschema.Validate(set.InputSchema, []byte(raw)))
	}
	for _, name := range InteractionHandlerToolNames() {
		require.True(t, IsInteractionHandlerTool(name))
	}
	_, ok = catalog.Lookup("slack_post_message")
	require.False(t, ok)
}
