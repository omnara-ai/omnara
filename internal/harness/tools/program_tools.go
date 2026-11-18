package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func AgentConfigToolSpecs(
	ctx context.Context, store *storage.Store, projectID, agentID, agentConfigID uuid.UUID,
) ([]modelcontext.ToolSpec, agentconfig.RuntimeContract, error) {
	contract, err := agentConfigContract(ctx, store, projectID, agentConfigID)
	if err != nil {
		return nil, contract, err
	}
	specs, err := modelcontext.RuntimeContractToolSpecs(ctx, modelContextStore(store), projectID, agentID, contract,
		time.Now())
	return specs, contract, err
}

func AgentConfigToolSpec(
	ctx context.Context, store *storage.Store, projectID, agentID, agentConfigID uuid.UUID, name string,
) (modelcontext.ToolSpec, agentconfig.RuntimeContract, bool, error) {
	contract, err := agentConfigContract(ctx, store, projectID, agentConfigID)
	if err != nil {
		return modelcontext.ToolSpec{}, contract, false, err
	}
	spec, found, err := modelcontext.RuntimeContractToolSpec(ctx, modelContextStore(store), projectID, agentID,
		contract, time.Now(), name)
	return spec, contract, found, err
}

func agentConfigContract(
	ctx context.Context, store *storage.Store, projectID, agentConfigID uuid.UUID,
) (agentconfig.RuntimeContract, error) {
	config, found, err := store.Execution().GetAgentConfig(ctx, projectID, agentConfigID)
	if err != nil {
		return agentconfig.RuntimeContract{}, err
	}
	if !found {
		return agentconfig.RuntimeContract{}, fmt.Errorf("agent config %s not found", agentConfigID)
	}
	return agentconfig.RuntimeContractFromCompiled(config.CompiledDefinition, config.EffectiveDefinitionHash)
}

func modelContextStore(store *storage.Store) modelcontext.Store {
	return modelcontext.NewStore(store.Execution(), store.Artifacts(), store.Integrations())
}

const programToolCallInternalError = "The tool call failed because of an internal error."

func ProgramToolSearch(
	specs []modelcontext.ToolSpec, input json.RawMessage,
) (modelcontext.ProgramToolSearchResult, error) {
	request, err := resolveToolSearchRequest(input)
	if err != nil {
		return modelcontext.ProgramToolSearchResult{}, err
	}
	if request.MaxResults <= 0 {
		request.MaxResults = toolcatalog.ToolSearchMaxResults
	}
	return modelcontext.SearchTools(specs, request.Pattern, request.MaxResults)
}

func (e Executor) RunProgramToolCall(
	ctx context.Context, claim executionstore.ProgramToolCallClaim, lease time.Duration,
) error {
	if e.Store == nil {
		return errors.New("tool executor store is required")
	}
	turn, err := e.programTurn(ctx, claim, lease)
	if err == nil {
		err = e.runProgramPhase(ctx, turn, claim)
	}
	if err == nil || errors.Is(err, storeerr.ErrRuntimeLockInactive) {
		return err
	}
	if ctx.Err() != nil {
		return errors.Join(err, e.releaseInterruptedProgramToolCall(ctx, turn, claim))
	}
	parts, contentErr := structuredParts(map[string]any{
		"error_code": "tool_call_failed", "error": programToolCallInternalError,
	})
	if contentErr != nil {
		return errors.Join(err, contentErr)
	}
	if _, completeErr := turn.toolAuthority().complete(ctx, e, turn, claim.ID, executionstore.ToolResultOutcomeFailed,
		parts); completeErr != nil && !errors.Is(completeErr, storeerr.ErrRuntimeLockInactive) {
		return errors.Join(err, completeErr)
	}
	return err
}

func (e Executor) releaseInterruptedProgramToolCall(
	ctx context.Context, turn Turn, claim executionstore.ProgramToolCallClaim,
) error {
	authority, ok := turn.authority.(programClaimAuthority)
	if !ok || !authority.interrupted() {
		return nil
	}
	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), asyncToolCompletionTimeout)
	defer cancel()
	err := authority.requeue(releaseCtx, e, turn, claim.ID)
	if errors.Is(err, storeerr.ErrRuntimeLockInactive) || errors.Is(err, storeerr.ErrNotFound) {
		return nil
	}
	return err
}

func (e Executor) runProgramPhase(ctx context.Context, turn Turn, claim executionstore.ProgramToolCallClaim) error {
	record := claim.ToolCallRecord
	switch claim.Phase {
	case executionstore.ProgramToolCallPhaseAuthorize:
		authorized, err := e.PrepareRecordedToolCallPermission(ctx, turn, record)
		if err != nil || authorized.State != executionstore.ToolCallStateWaiting {
			return err
		}
		_, err = e.DispatchRecordedToolCall(ctx, turn, authorized)
		return err
	case executionstore.ProgramToolCallPhaseExecute:
		approved, err := e.programApprovalHolds(ctx, turn, record)
		if err != nil || !approved {
			return err
		}
		_, err = e.DispatchRecordedToolCall(ctx, turn, record)
		return err
	default:
		return fmt.Errorf("unsupported program tool call phase %d", claim.Phase)
	}
}

func (e Executor) programTurn(
	ctx context.Context, claim executionstore.ProgramToolCallClaim, lease time.Duration,
) (Turn, error) {
	authority := programClaimAuthority{ref: claim.Ref(), lease: lease, stopped: ctx.Done()}
	if claim.Type != toolcatalog.ToolTypeBuiltIn {
		authority.started = new(atomic.Bool)
	}
	turn := Turn{ProjectID: claim.ProjectID, AgentID: claim.AgentID, SourceEventID: claim.SourceEventID,
		ModelCallContextID: claim.ModelCallContextID, authority: authority}
	contextRecord, found, err := e.Store.Execution().
		GetModelCallContext(ctx, claim.ProjectID, claim.AgentID, claim.ModelCallContextID)
	if err != nil {
		return turn, err
	}
	if !found {
		return turn, fmt.Errorf("model call context %s not found", claim.ModelCallContextID)
	}
	turn.OrgID = contextRecord.OrgID
	agent, err := e.Store.Execution().GetAgentInProject(ctx, claim.ProjectID, claim.AgentID)
	if err != nil {
		return turn, err
	}
	spec, contract, found, err := AgentConfigToolSpec(
		ctx, e.Store, claim.ProjectID, claim.AgentID, agent.CurrentConfigID, claim.Name)
	if err != nil {
		return turn, err
	}
	turn.contract = &contract
	turn.Tools = map[string]ToolSpec{}
	if found && modelcontext.ProgramCallable(spec) {
		turn.Tools[spec.Name] = ToolSpecFromModelContext(spec)
	}
	return turn, nil
}
