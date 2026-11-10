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
)

// Program tool calls are tools a run_command process calls through
// `omnarad call`. They run under a claim with the permissions of the
// agent's current config, and never enter model context.

// AgentConfigToolSpecs returns the runtime tools of an agent config, as its
// model calls see them.
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

// AgentConfigToolSpec is AgentConfigToolSpecs for the tool name alone. It
// loads only what that tool needs.
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

// ProgramToolSearch answers `omnarad call tool_search`. It takes tool_search's
// input but returns every match unless max_results is set.
func ProgramToolSearch(
	specs []modelcontext.ToolSpec, input json.RawMessage,
) (modelcontext.ProgramToolSearchResult, error) {
	request, err := resolveToolSearchRequest(input)
	if err != nil {
		return modelcontext.ProgramToolSearchResult{}, err
	}
	return modelcontext.SearchTools(specs, request.Pattern, request.MaxResults)
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
	turn, err := e.programTurn(ctx, claim, lease)
	if err == nil {
		err = e.runProgramPhase(ctx, turn, claim)
	}
	if err == nil || errors.Is(err, storeerr.ErrRuntimeLockInactive) || ctx.Err() != nil {
		return err
	}
	// Fail the call rather than leave it to be re-claimed into the same error.
	// The command gets a fixed message; the caller logs the error.
	completeErr := e.completeInvalidToolCall(ctx, turn, claim.ID, "tool_call_failed",
		errors.New(programToolCallInternalError))
	if completeErr != nil && !errors.Is(completeErr, storeerr.ErrRuntimeLockInactive) {
		return errors.Join(err, completeErr)
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
		// An allowed call is left waiting under this claim: run it now.
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

// programTurn is the turn a program call acts in: the parent's model call
// context, the called tool if commands may call it under the agent's current
// config, and the claim as its authority.
func (e Executor) programTurn(
	ctx context.Context, claim executionstore.ProgramToolCallClaim, lease time.Duration,
) (Turn, error) {
	turn := Turn{ProjectID: claim.ProjectID, AgentID: claim.AgentID, SourceEventID: claim.SourceEventID,
		ModelCallContextID: claim.ModelCallContextID,
		authority:          programClaimAuthority{ref: claim.Ref(), lease: lease}}
	agent, err := e.Store.Execution().GetAgentInProject(ctx, claim.ProjectID, claim.AgentID)
	if err != nil {
		return turn, err
	}
	turn.OrgID = agent.OrgID
	spec, contract, found, err := AgentConfigToolSpec(
		ctx, e.Store, claim.ProjectID, claim.AgentID, agent.CurrentConfigID, claim.Name)
	if err != nil {
		return turn, err
	}
	turn.contract = &contract
	turn.Tools = map[string]ToolSpec{}
	if found && modelcontext.ProgramCallable(contract, spec) {
		turn.Tools[spec.Name] = ToolSpecFromModelContext(spec)
	}
	return turn, nil
}
