package executionstore

import (
	"slices"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookRetryable(t *testing.T) {
	require.False(t, eventWebhookRetryable(nil, "", ""))
	for _, tool := range []struct {
		kind   string
		name   string
		states []ToolCallState
	}{
		{toolcatalog.ToolTypeCustom, "external_lookup", []ToolCallState{ToolCallStateReady, ToolCallStateAwaitingPermission}},
		{toolcatalog.ToolTypeBuiltIn, toolcatalog.ToolNameAskQuestion,
			[]ToolCallState{ToolCallStateRunning, ToolCallStateAwaitingPermission}},
		{toolcatalog.ToolTypeBuiltIn, toolcatalog.ToolNameRunCommand, []ToolCallState{ToolCallStateAwaitingPermission}},
		{toolcatalog.ToolTypeMCP, toolcatalog.ToolNameAskQuestion, []ToolCallState{ToolCallStateAwaitingPermission}},
		{toolcatalog.ToolTypeCustom, toolcatalog.ToolNameAskQuestion,
			[]ToolCallState{ToolCallStateReady, ToolCallStateAwaitingPermission}},
	} {
		for _, state := range []ToolCallState{
			ToolCallStateAwaitingAuthorization, ToolCallStateAwaitingPermission, ToolCallStateReady,
			ToolCallStateRunning, ToolCallStateWaiting, ToolCallStateCompleted,
		} {
			value := string(state)
			require.Equal(t, slices.Contains(tool.states, state), eventWebhookRetryable(&value, tool.kind, tool.name),
				"%s %s %s", tool.kind, tool.name, state)
		}
	}
}
