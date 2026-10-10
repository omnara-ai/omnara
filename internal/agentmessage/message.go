package agentmessage

import (
	"fmt"
	"strings"
)

func Render(displayName, key, childPublicID, kind, text string) string {
	label := fmt.Sprintf(
		"Subagent %q (agent_id %s, key %q)", displayName, childPublicID, key,
	)
	var header string
	switch kind {
	case "result":
		header = label + " finished its turn:"
	case "refused":
		header = label + " stopped because its model refused to continue; it produced no final answer:"
	case "content_filtered":
		header = label + " stopped because its model output was blocked by a content filter; " +
			"it produced no final answer:"
	case "failed":
		header = label + " failed:"
	case "question":
		header = label + " asked a question and is paused until a human answers it. " +
			"Messaging it with send_agent_message cancels the question."
	default:
		header = label + ":"
	}
	if strings.TrimSpace(text) == "" {
		return header
	}
	return header + "\n\n" + text
}
