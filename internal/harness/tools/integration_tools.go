package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func integrationToolImplementation(name string) (toolImplementation, bool) {
	if _, _, ok := toolcatalog.SplitIntegrationToolName(name); !ok {
		return toolImplementation{}, false
	}
	return toolImplementation{
		toolRegistration: toolRegistration{
			name:            name,
			handler:         toolHandler{Async: runIntegrationTool},
			permissionModes: commonPermissionModeHandlers(integrationPermissionChallenge),
		},
		permissionDescriptors: toolpermission.CommonModeDescriptors(),
	}, true
}

func runIntegrationTool(ctx context.Context, call asyncToolContext) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().
		GetToolCall(ctx, call.Turn.ProjectID, call.Turn.AgentID, call.ToolCallID)
	if err != nil {
		return nil, err
	}
	access, err := call.Executor.resolveIntegrationToolAccess(ctx, call.Turn, record)
	if err != nil {
		return integrationToolFailure(err)
	}
	switch access.Integration.IntegrationType {
	case integrationdefinition.SlackThread:
		return runSlackTool(ctx, call, record, access)
	case integrationdefinition.DiscordThread:
		return runDiscordTool(ctx, call, record, access)
	case integrationdefinition.GitHubPR:
		return runGitHubTool(ctx, call, record, access)
	default:
		return integrationToolFailure(fmt.Errorf("unsupported integration type %q", access.Integration.IntegrationType))
	}
}

func integrationToolPreparationFailure(err error) error {
	content, marshalErr := structuredToolResultContent(map[string]string{
		"code": "integration_tool_failed", "message": err.Error(),
	})
	if marshalErr != nil {
		return marshalErr
	}
	return newToolCallPreparationError(content, err)
}

func integrationPermissionChallenge(
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
	access, err := e.resolveIntegrationToolAuthority(ctx, turn, record)
	if err != nil {
		return toolpermission.Request{}, err
	}
	access.Conversation, err = e.integrationToolConversation(ctx, turn, access)
	if err != nil {
		return toolpermission.Request{}, err
	}
	summary, err := integrationToolPermissionSummary(access, call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, summary)
}

func integrationToolPermissionSummary(access integrationToolAccess, input json.RawMessage) (json.RawMessage, error) {
	var destination json.RawMessage
	switch access.Authority.Definition.Scope {
	case toolcatalog.IntegrationToolScopeIntegration:
	case toolcatalog.IntegrationToolScopeConversation:
		var err error
		destination, err = access.Conversation.ConversationJSON()
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("integration tool requires an explicit scope")
	}
	return json.Marshal(struct {
		Destination json.RawMessage `json:"destination,omitempty"`
		Arguments   json.RawMessage `json:"arguments"`
	}{destination, input})
}
