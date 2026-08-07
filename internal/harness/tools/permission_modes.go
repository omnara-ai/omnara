package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type permissionModeResultKind uint8

const (
	permissionModeInvalid permissionModeResultKind = iota
	permissionModeAllow
	permissionModeDeny
	permissionModeAsk
)

type permissionModeResult struct {
	kind    permissionModeResultKind
	request toolpermission.Request
	reason  string
}

type toolCallPreparationError struct {
	content toolResultContent
	cause   error
}

func (e *toolCallPreparationError) Error() string {
	return e.cause.Error()
}

func (e *toolCallPreparationError) Unwrap() error {
	return e.cause
}

func newToolCallPreparationError(
	content toolResultContent,
	cause error,
) error {
	return &toolCallPreparationError{content: content, cause: cause}
}

type permissionModeContext struct {
	selection  toolpermission.Selection
	descriptor toolpermission.ModeDescriptor
}

type permissionModeHandler func(
	context.Context,
	Executor,
	Turn,
	model.ToolCall,
	permissionModeContext,
) (permissionModeResult, error)

type permissionChallengeBuilder func(
	context.Context,
	Executor,
	Turn,
	model.ToolCall,
	permissionModeContext,
) (toolpermission.Request, error)

func commonPermissionModeHandlers(
	challengeBuilder permissionChallengeBuilder,
) map[string]permissionModeHandler {
	return map[string]permissionModeHandler{
		toolpermission.ModeAlwaysAllow: allowPermissionMode,
		toolpermission.ModeAlwaysAsk:   askPermissionMode(challengeBuilder),
		toolpermission.ModeAlwaysDeny:  denyPermissionMode,
	}
}

func alwaysAllowPermissionModeHandlers() map[string]permissionModeHandler {
	return map[string]permissionModeHandler{
		toolpermission.ModeAlwaysAllow: allowPermissionMode,
	}
}

func allowPermissionMode(
	_ context.Context,
	_ Executor,
	_ Turn,
	_ model.ToolCall,
	_ permissionModeContext,
) (permissionModeResult, error) {
	return permissionModeResult{kind: permissionModeAllow}, nil
}

func denyPermissionMode(
	_ context.Context,
	_ Executor,
	_ Turn,
	_ model.ToolCall,
	_ permissionModeContext,
) (permissionModeResult, error) {
	return permissionModeResult{
		kind:   permissionModeDeny,
		reason: "agent config permission mode denied this tool call",
	}, nil
}

func askPermissionMode(builder permissionChallengeBuilder) permissionModeHandler {
	return func(
		ctx context.Context,
		executor Executor,
		turn Turn,
		call model.ToolCall,
		mode permissionModeContext,
	) (permissionModeResult, error) {
		request, err := builder(ctx, executor, turn, call, mode)
		if err != nil {
			return permissionModeResult{}, err
		}
		return permissionModeResult{kind: permissionModeAsk, request: request}, nil
	}
}

func genericPermissionChallenge(
	_ context.Context,
	_ Executor,
	_ Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	var input map[string]json.RawMessage
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return toolpermission.Request{}, fmt.Errorf("tool input must be a JSON object: %w", err)
	}
	return permissionChallenge(call, mode, call.Input)
}

func setIntegrationTargetPermissionChallenge(
	_ context.Context,
	_ Executor,
	_ Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	input, err := resolveIntegrationTargetRequest(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	authorizationInput, err := marshalJSON(input)
	if err != nil {
		return toolpermission.Request{}, fmt.Errorf("marshal integration target authorization: %w", err)
	}
	return permissionChallenge(call, mode, authorizationInput)
}

func writeProcessPermissionChallenge(
	_ context.Context,
	_ Executor,
	_ Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	processID, payload, err := writeProcessPayload(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	authorizationInput, err := processActionAuthorizationRequest(
		processID,
		executionstore.ProcessActionKindWrite,
		payload,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorizationInput)
}

func stopProcessPermissionChallenge(
	_ context.Context,
	_ Executor,
	_ Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	processID, kind, err := resolveStopProcessRequest(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	authorizationInput, err := processActionAuthorizationRequest(
		processID,
		kind,
		json.RawMessage(`{}`),
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorizationInput)
}

func processObservationPermissionChallenge(
	observationMode processObservationMode,
) permissionChallengeBuilder {
	return func(
		_ context.Context,
		_ Executor,
		_ Turn,
		call model.ToolCall,
		mode permissionModeContext,
	) (toolpermission.Request, error) {
		request, err := resolveProcessObservationRequest(call.Input, observationMode)
		if err != nil {
			return toolpermission.Request{}, err
		}
		authorizationInput, err := marshalJSON(request)
		if err != nil {
			return toolpermission.Request{}, fmt.Errorf(
				"marshal %s_process authorization: %w",
				observationMode,
				err,
			)
		}
		return permissionChallenge(call, mode, authorizationInput)
	}
}

func createMachinePermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	if executor.Store == nil {
		return toolpermission.Request{}, fmt.Errorf("tool executor store is required")
	}
	input, err := resolveCreateMachineRequest(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	contextRecord, found, err := executor.Store.Execution().GetModelCallContext(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.ModelCallContextID,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	if !found {
		return toolpermission.Request{}, fmt.Errorf("model call context not found")
	}
	sources, err := executor.Store.Execution().ListMachinePoolSources(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		contextRecord.AgentConfigID,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	source, err := selectPoolForMachineCreate(sources, input)
	if err != nil {
		content, contentErr := machineToolFailureContent(
			"create_machine_failed",
			err.Error(),
			false,
		)
		if contentErr != nil {
			return toolpermission.Request{}, contentErr
		}
		return toolpermission.Request{}, newToolCallPreparationError(content, err)
	}
	authorizationInput, err := machineCreateAuthorizationInput(source.MachinePoolName)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorizationInput)
}

func listMachinesPermissionChallenge(
	_ context.Context,
	_ Executor,
	_ Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	authorizationInput, err := machineObservationAuthorizationInput(machineObservationList, "")
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorizationInput)
}

func inspectMachinePermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	input, err := resolveMachineRefRequest(call.Input, true)
	if err != nil {
		return toolpermission.Request{}, err
	}
	machineRef := input.MachineRef
	if machineRef == "" {
		if executor.Store == nil {
			return toolpermission.Request{}, fmt.Errorf("tool executor store is required")
		}
		machines, err := executor.Store.Execution().ListPoolMachines(
			ctx,
			turn.ProjectID,
			turn.AgentID,
		)
		if err != nil {
			return toolpermission.Request{}, err
		}
		machine, err := selectOnlyMachine(machines)
		if err != nil {
			return toolpermission.Request{}, executor.machinePreparationError(err)
		}
		machineRef = machine.Binding.MachineRef
	}
	authorizationInput, err := machineObservationAuthorizationInput(
		machineObservationInspect,
		machineRef,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return permissionChallenge(call, mode, authorizationInput)
}

func permissionChallenge(
	call model.ToolCall,
	mode permissionModeContext,
	authorizationInput json.RawMessage,
) (toolpermission.Request, error) {
	authorization, err := toolpermission.NewAuthorization(
		call.Name,
		authorizationInput,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	input := strings.TrimSpace(string(authorization.Input))
	contextItems := []interactionform.ContextItem(nil)
	if input != "" && input != "{}" {
		contextItems = []interactionform.ContextItem{
			{Label: "Input", Value: input},
		}
	}
	value, err := toolpermission.NewAllowDenyForm(
		"Permission requested for "+call.Name,
		contextItems,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return toolpermission.NewRequest(
		mode.descriptor,
		mode.selection,
		authorization,
		value,
	)
}

func runCommandPermissionChallenge(
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	mode permissionModeContext,
) (toolpermission.Request, error) {
	resolved, err := resolveRunCommandRequest(call.Input)
	if err != nil {
		return toolpermission.Request{}, err
	}
	binding, err := executor.waitForMachineExecutionTarget(ctx, turn, resolved.MachineRef)
	if err != nil {
		return toolpermission.Request{}, executor.machinePreparationError(err)
	}
	authorizationInput, err := runCommandAuthorizationInput(binding.ID, resolved)
	if err != nil {
		return toolpermission.Request{}, err
	}
	authorization, err := toolpermission.NewAuthorization(
		call.Name,
		authorizationInput,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	contextItems := []interactionform.ContextItem{
		{Label: "Command", Value: resolved.Command},
		{Label: "Machine", Value: binding.MachineRef},
		{Label: "Shell", Value: string(resolved.Selector)},
	}
	if resolved.Cwd != "" {
		contextItems = append(
			contextItems,
			interactionform.ContextItem{Label: "Working directory", Value: resolved.Cwd},
		)
	}
	value, err := toolpermission.NewAllowDenyForm(
		"Permission requested for run_command",
		contextItems,
	)
	if err != nil {
		return toolpermission.Request{}, err
	}
	return toolpermission.NewRequest(
		mode.descriptor,
		mode.selection,
		authorization,
		value,
	)
}

func (e Executor) machinePreparationError(cause error) error {
	unavailable, err := machineUnavailableToolResult(cause)
	if err != nil {
		return err
	}
	return newToolCallPreparationError(unavailable.Content, unavailable.Cause)
}

func permissionModeForTool(
	toolType string,
	selection toolpermission.Selection,
	implementation toolImplementation,
	implemented bool,
) (permissionModeHandler, toolpermission.ModeDescriptor, bool, error) {
	if toolType == toolcatalog.ToolTypeCustom {
		handler := commonPermissionModeHandlers(genericPermissionChallenge)[selection.Mode]
		descriptor, ok := toolpermission.FindMode(
			toolcatalog.CustomToolPermissionModes(),
			selection.Mode,
		)
		return handler, descriptor, ok && handler != nil, nil
	}
	if !implemented {
		return nil, toolpermission.ModeDescriptor{}, false, nil
	}
	handler := implementation.permissionModes[selection.Mode]
	descriptor, ok := toolpermission.FindMode(
		implementation.permissionDescriptors,
		selection.Mode,
	)
	return handler, descriptor, ok && handler != nil, nil
}
