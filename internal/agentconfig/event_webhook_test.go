package agentconfig

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentConfigRejectsEventWebhook(t *testing.T) {
	source := validAgentSource("event_webhook:\n  url: https://example.com/events\n  events: [tool_call_update]\n")
	_, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	require.ErrorContains(t, err, "event_webhook")
}
