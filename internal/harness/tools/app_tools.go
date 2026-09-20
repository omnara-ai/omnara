package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func appToolImplementation(name string) (toolImplementation, bool) {
	if _, _, ok := toolcatalog.SplitAppToolName(name); !ok {
		return toolImplementation{}, false
	}
	return toolImplementation{
		toolRegistration: toolRegistration{
			name:            name,
			handler:         toolHandler{Async: runAppTool},
			permissionModes: commonPermissionModeHandlers(appPermissionChallenge),
		},
		permissionDescriptors: toolpermission.CommonModeDescriptors(),
	}, true
}

func runAppTool(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().
		GetToolCall(ctx, call.Turn.ProjectID, call.Turn.AgentID, call.ToolCallID)
	if err != nil {
		return nil, err
	}
	access, err := call.Executor.resolveAppToolAccess(ctx, call.Turn, record)
	if err != nil {
		return appToolFailure(err)
	}
	switch access.App.Provider {
	case appdefinition.ProviderSlack:
		return runSlackTool(ctx, call, record, access)
	case appdefinition.ProviderDiscord:
		return runDiscordTool(ctx, call, record, access)
	case appdefinition.ProviderGitHub:
		return runGitHubTool(ctx, call, record, access)
	default:
		return appToolFailure(fmt.Errorf("unsupported app provider %q", access.App.Provider))
	}
}

// Hidden config must not hide the action's destination from an approver.
func appPermissionChallenge(
	ctx context.Context,
	e Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	record, err := e.recordedToolCall(ctx, turn, call)
	if err != nil {
		return toolpermission.Request{}, err
	}
	access, err := e.resolveAppToolScope(ctx, turn, record)
	if err != nil {
		return toolpermission.Request{}, err
	}
	description, err := access.Authority.Definition.Describe(access.Authority.Tool.Config)
	if err != nil {
		return toolpermission.Request{}, err
	}
	summary, err := json.Marshal(struct {
		Description string          `json:"description"`
		Arguments   json.RawMessage `json:"arguments"`
	}{description, call.Input})
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, summary)
}
