package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

// Program tool calls are tools a run_command process calls through
// `omnarad call`. They run under a worker claim with the permissions of the
// config the process's model call ran under, and never enter model context.

// programReadOnlyBuiltInTools are the built-in tools commands may call in v1.
var programReadOnlyBuiltInTools = map[string]bool{
	toolcatalog.ToolNameStorageRead:   true,
	toolcatalog.ToolNameStorageList:   true,
	toolcatalog.ToolNameStorageSearch: true,
	toolcatalog.ToolNameWebSearch:     true,
	toolcatalog.ToolNameWebFetch:      true,
}

// ProgramToolCallable reports whether commands may call spec: the config opts
// in and does not exclude it, and it is an MCP tool, a read-only built-in, or
// an integration read.
func ProgramToolCallable(contract agentconfig.RuntimeContract, spec modelcontext.ToolSpec) bool {
	if contract.ProgrammaticToolExcluded(spec.Name) {
		return false
	}
	switch spec.Type {
	case toolcatalog.ToolTypeMCP:
		return toolcatalog.IsMCPRuntimeToolName(spec.Name)
	case toolcatalog.ToolTypeBuiltIn:
		if _, operation, ok := toolcatalog.SplitIntegrationToolName(spec.Name); ok {
			return operation == toolcatalog.IntegrationOperationRead
		}
		return programReadOnlyBuiltInTools[spec.Name]
	default:
		return false
	}
}

// AgentConfigToolSpecs returns the runtime tools of an agent config, as its
// model calls see them.
func AgentConfigToolSpecs(
	ctx context.Context, store *storage.Store, projectID, agentID, agentConfigID uuid.UUID,
) ([]modelcontext.ToolSpec, agentconfig.RuntimeContract, error) {
	config, found, err := store.Execution().GetAgentConfig(ctx, projectID, agentConfigID)
	if err != nil {
		return nil, agentconfig.RuntimeContract{}, err
	}
	if !found {
		return nil, agentconfig.RuntimeContract{}, fmt.Errorf("agent config %s not found", agentConfigID)
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(config.CompiledDefinition, config.EffectiveDefinitionHash)
	if err != nil {
		return nil, agentconfig.RuntimeContract{}, err
	}
	specs, err := modelcontext.RuntimeContractToolSpecs(ctx,
		modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations()),
		projectID, agentID, contract, time.Now())
	return specs, contract, err
}

// ProgramToolSpecs returns the tools commands may call under the agent config
// a process's model call ran with.
func ProgramToolSpecs(
	ctx context.Context, store *storage.Store, projectID, agentID, agentConfigID uuid.UUID,
) ([]modelcontext.ToolSpec, error) {
	specs, contract, err := AgentConfigToolSpecs(ctx, store, projectID, agentID, agentConfigID)
	if err != nil {
		return nil, err
	}
	callable := specs[:0]
	for _, spec := range specs {
		if ProgramToolCallable(contract, spec) {
			callable = append(callable, spec)
		}
	}
	return callable, nil
}

// ProgramToolSearch answers `omnarad call tool_search`. It takes tool_search's
// input but returns every match unless max_results is set.
func ProgramToolSearch(
	specs []modelcontext.ToolSpec, input json.RawMessage,
) (modelcontext.ProgramToolSearchResult, error) {
	request, err := resolveToolSearchRequest(input)
	if err != nil {
		return modelcontext.ProgramToolSearchResult{}, err
	}
	var raw toolSearchRequest
	if err := json.Unmarshal(input, &raw); err != nil {
		return modelcontext.ProgramToolSearchResult{}, err
	}
	maxResults := 0
	if len(raw.MaxResults) != 0 {
		maxResults = request.MaxResults
	}
	return modelcontext.SearchTools(specs, request.Pattern, maxResults)
}

// RunProgramToolCall runs the claimed phase of a program tool call: the
// permission step, or execution. An async tool keeps running after it returns
// under the context's async execution scope; the caller renews the claim until
// that scope is done.
func (e Executor) RunProgramToolCall(
	ctx context.Context, claim executionstore.ProgramToolCallClaim, lease time.Duration,
) error {
	if e.Store == nil {
		return errors.New("tool executor store is required")
	}
	record := claim.ToolCallRecord
	turn, err := e.programTurn(ctx, claim, lease)
	if err == nil {
		switch claim.Phase {
		case executionstore.ProgramToolCallPhaseAuthorize:
			_, err = e.PrepareRecordedToolCallPermission(ctx, turn, record)
		case executionstore.ProgramToolCallPhaseExecute:
			_, err = e.DispatchRecordedToolCall(ctx, turn, record)
		default:
			err = fmt.Errorf("unsupported program tool call phase %d", claim.Phase)
		}
	}
	if err == nil || errors.Is(err, storeerr.ErrRuntimeLockInactive) || ctx.Err() != nil {
		return err
	}
	// Fail the call rather than leave it to be re-claimed into the same error.
	content, contentErr := structuredToolResultContent(map[string]any{
		"error_code": "tool_call_failed", "error": err.Error(),
	})
	if contentErr != nil {
		return errors.Join(err, contentErr)
	}
	parts, contentErr := content.contentParts()
	if contentErr != nil {
		return errors.Join(err, contentErr)
	}
	authority := programClaimAuthority{ref: programToolCallRef(claim), lease: lease}
	if _, completeErr := authority.complete(ctx, e, turn, record.ID, executionstore.ToolResultOutcomeFailed,
		parts); completeErr != nil && !errors.Is(completeErr, storeerr.ErrRuntimeLockInactive) {
		return errors.Join(err, completeErr)
	}
	return err
}

func programToolCallRef(claim executionstore.ProgramToolCallClaim) executionstore.ProgramToolCallRef {
	return executionstore.ProgramToolCallRef{ProjectID: claim.ProjectID, AgentID: claim.AgentID,
		ProcessID: claim.ProcessID, ToolCallID: claim.ID, ClaimID: claim.ClaimID}
}

// programTurn is the turn a program call acts in: the parent's model call
// context, the tools commands may call, and the claim as its authority.
func (e Executor) programTurn(
	ctx context.Context, claim executionstore.ProgramToolCallClaim, lease time.Duration,
) (Turn, error) {
	turn := Turn{ProjectID: claim.ProjectID, AgentID: claim.AgentID, SourceEventID: claim.SourceEventID,
		ModelCallContextID: claim.ModelCallContextID,
		authority:          programClaimAuthority{ref: programToolCallRef(claim), lease: lease}}
	contextRecord, found, err := e.Store.Execution().
		GetModelCallContext(ctx, claim.ProjectID, claim.AgentID, claim.ModelCallContextID)
	if err != nil {
		return turn, err
	}
	if !found {
		return turn, fmt.Errorf("model call context %s not found", claim.ModelCallContextID)
	}
	turn.OrgID = contextRecord.OrgID
	specs, err := ProgramToolSpecs(ctx, e.Store, claim.ProjectID, claim.AgentID, contextRecord.AgentConfigID)
	if err != nil {
		return turn, err
	}
	turn.Tools = make(map[string]ToolSpec, len(specs))
	for _, spec := range specs {
		turn.Tools[spec.Name] = ToolSpec{Type: spec.Type, Permission: spec.Permission,
			Description: spec.Description, InputSchema: spec.InputSchema, Deferred: spec.Deferred}
	}
	return turn, nil
}
