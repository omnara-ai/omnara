package tools

import (
	"context"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func PlanDatabaseTool(
	claim executionstore.ClaimedAgentWork, call executionstore.ToolCallRecord,
) (executionstore.DatabaseToolPlan, bool, error) {
	implementation, found, err := toolImplementationFor(call.Name)
	if err != nil || !found {
		return nil, false, err
	}
	if implementation.completion != completionBoundedDatabase || implementation.locks != locksCallingAgent ||
		call.Type != toolcatalog.ToolTypeBuiltIn || claim.Tool.Prepared == nil ||
		call.Name == toolcatalog.ToolNameListInteractionHandlers {
		return nil, false, nil
	}
	config := claim.Tool.Prepared.Config
	contract, err := agentconfig.RuntimeContractFromCompiled(config.CompiledDefinition, config.EffectiveDefinitionHash)
	if err != nil {
		return nil, false, err
	}
	allowed := false
	for _, spec := range contract.Tools {
		if spec.Name != call.Name || spec.Type != toolcatalog.ToolTypeBuiltIn {
			continue
		}
		if spec.Permission.Mode != toolpermission.ModeAlwaysAllow {
			return nil, false, nil
		}
		_, selectionErr := toolpermission.ValidateSelection(spec.Permission, implementation.permissionDescriptors)
		allowed = selectionErr == nil
	}
	validInput := implementation.validateInput(call.Input) == nil
	if !allowed || !validInput {
		return nil, false, nil
	}
	return func(
		ctx context.Context, reader *executionstore.ToolCallReader,
	) (executionstore.ToolCallCompletionInput, bool, error) {
		specs, err := modelcontext.RuntimeContractToolSpecs(
			ctx, reader, claim.ProjectID, claim.AgentID, contract, claim.RuntimeLock.StartedAt,
		)
		if err != nil {
			return executionstore.ToolCallCompletionInput{}, false, err
		}
		turn := Turn{
			RootAgentID: claim.RootAgentID, OrgID: claim.OrgID, ProjectID: claim.ProjectID, AgentID: claim.AgentID,
			SourceEventID: claim.Tool.SourceEventID, RuntimeLockID: claim.RuntimeLock.ID,
			ModelCallContextID: claim.Tool.ModelCallContextID, Tools: make(map[string]ToolSpec, len(specs)),
		}
		for _, spec := range specs {
			turn.Tools[spec.Name] = ToolSpec{Type: spec.Type, Permission: spec.Permission, Description: spec.Description,
				InputSchema: spec.InputSchema, Deferred: spec.Deferred}
		}
		phase, err := invokeTransactionalToolHandler(ctx, implementation.handler.Transactional, transactionalToolContext{
			Reader: reader, Turn: turn, ToolCallID: call.ID,
			Call: model.ToolCall{ID: call.ProviderCallID, Name: call.Name, Input: call.Input},
		})
		if err != nil {
			return executionstore.ToolCallCompletionInput{}, false, err
		}
		completed, ok := phase.(completeTransaction)
		if !ok {
			return executionstore.ToolCallCompletionInput{}, false, nil
		}
		completion, err := successfulToolCallCompletion(completed.content)
		return completion, err == nil, err
	}, true, nil
}
