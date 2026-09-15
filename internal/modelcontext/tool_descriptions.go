package modelcontext

import "github.com/omnara-ai/omnara/internal/toolpermission"

func approvalDescription(spec ToolSpec, canSelectChannel bool) string {
	var notice string
	switch spec.Permission.Mode {
	case "", toolpermission.ModeAlwaysAllow, toolpermission.ModeAlwaysDeny:
		return spec.Description
	case toolpermission.ModeAlwaysAsk:
		notice = "Requires approval before execution."
	default:
		notice = "May require approval before execution."
	}
	if canSelectChannel {
		notice += " Use set_current_channel to choose where approval prompts are sent."
	}
	if spec.Description == "" {
		return notice
	}
	return spec.Description + " " + notice
}
