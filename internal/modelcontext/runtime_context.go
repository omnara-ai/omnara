package modelcontext

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func IntegrationTargetContextEnabled(specs []ToolSpec) bool {
	return HasAnyTool(
		specs,
		toolcatalog.ToolNameSendIntegrationMessage,
		toolcatalog.ToolNameSetIntegrationTarget,
	)
}

func IntegrationTargetsContent(targets []IntegrationTargetRef) string {
	if len(targets) == 0 {
		return "No external integration targets are currently available for this agent."
	}
	body, err := json.Marshal(targets)
	if err != nil {
		return "External integration targets are present but could not be serialized."
	}
	return "External integration targets available for this agent. The current target is used by default " +
		"for outbound integration messages and interaction prompts: " + string(body)
}
