package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/sigv4"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type AgentExecutor struct {
	Store                    *storage.Store
	ContextBuilder           modelcontext.Builder
	ModelResolver            model.Resolver
	MCP                      mcp.Client
	MCPAuthHTTPClient        *http.Client
	SigV4CredentialCache     *sigv4.CredentialCache
	ToolExecutor             tools.Executor
	Now                      func() time.Time
	StreamPublisher          notifications.AgentStreamDeltaPublisher
	StreamLog                *slog.Logger
	MCPInitializationBackoff func(attempt int) time.Duration
	ModelRetryDelay          func(time.Duration) time.Duration
	OnModelFailure           func(ctx context.Context, projectID, agentID, runtimeLockID uuid.UUID) error
}

func (e AgentExecutor) ExecuteModelWork(ctx context.Context, input ModelWorkExecution) error {
	_, _, err := e.executeModelWork(ctx, input, nil)
	return err
}

func (e AgentExecutor) ExecuteModelWorkAndAdvance(
	ctx context.Context, input ModelWorkExecution, advance ModelWorkAdvanceOptions,
) (executionstore.OwnedAgentWorkTransition, bool, error) {
	return e.executeModelWork(ctx, input, &advance)
}

func (e AgentExecutor) executeModelWork(
	ctx context.Context, input ModelWorkExecution, advance *ModelWorkAdvanceOptions,
) (executionstore.OwnedAgentWorkTransition, bool, error) {
	if e.Store == nil {
		return executionstore.OwnedAgentWorkTransition{}, false, errors.New("kernel store is required")
	}
	if err := validateModelWorkExecution(input); err != nil {
		return executionstore.OwnedAgentWorkTransition{}, false, err
	}
	if input.Now.IsZero() {
		input.Now = e.now()
	}
	builder := e.contextBuilder()
	if e.ModelResolver == nil {
		return executionstore.OwnedAgentWorkTransition{}, false, errors.New("kernel model resolver is required")
	}

	if input.Kind == executionstore.ModelWorkResume {
		contextRow, found, loadErr := e.Store.Execution().GetModelCallContext(
			ctx,
			input.ProjectID,
			input.AgentID,
			input.ModelCallContextID,
		)
		if loadErr != nil {
			return executionstore.OwnedAgentWorkTransition{}, false, loadErr
		}
		if !found {
			return executionstore.OwnedAgentWorkTransition{}, false, errors.New("resume model context is missing")
		}
		if contextRow.OperationKind == executionstore.ModelCallOperationCompaction {
			return executionstore.OwnedAgentWorkTransition{}, false, e.resumeCompactionContext(
				ctx,
				input,
				builder,
				e.ModelResolver,
				contextRow,
			)
		}
	}

	step, err := e.executeModelStepWithAdvance(ctx, input, builder, e.ModelResolver, advance)
	if err != nil {
		return executionstore.OwnedAgentWorkTransition{}, false, err
	}
	if step.Transition != nil {
		return *step.Transition, true, nil
	}
	switch step.State {
	case modelStepWaiting, modelStepDone:
		return executionstore.OwnedAgentWorkTransition{}, false, nil
	case modelStepToolUse:
		if _, err := e.recordToolCallSourceEvent(
			ctx,
			input,
			step.Context,
			step.Response.ProviderRequestID,
			step.Envelope,
			step.Bundle.ToolSpecs,
			step.StreamedToolCallIDs,
		); err != nil {
			return executionstore.OwnedAgentWorkTransition{}, false, fmt.Errorf("record tool-call source event: %w", err)
		}
		return executionstore.OwnedAgentWorkTransition{}, false, nil
	default:
		return executionstore.OwnedAgentWorkTransition{}, false, fmt.Errorf("unsupported model step state %q", step.State)
	}
}

func (e AgentExecutor) notifyModelFailure(ctx context.Context, input ModelWorkExecution) {
	if e.OnModelFailure == nil || ctx.Err() != nil {
		return
	}
	postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := e.OnModelFailure(postCtx, input.ProjectID, input.AgentID, input.RuntimeLockID); err != nil {
		log := e.StreamLog
		if log == nil {
			log = slog.Default()
		}
		log.WarnContext(postCtx, "notify model failure", "agent_id", input.AgentID, "error", err)
	}
}

func validateModelWorkExecution(input ModelWorkExecution) error {
	if input.OrgID == uuid.Nil ||
		input.ProjectID == uuid.Nil ||
		input.AgentID == uuid.Nil ||
		input.TurnID == uuid.Nil ||
		input.RuntimeLockID == uuid.Nil ||
		len(input.InputIDs) == 0 ||
		input.OpeningEventSequence <= 0 {
		return errors.New(
			"model work organization, project, agent, turn, runtime lock, opening inputs, " +
				"and opening event sequence are required",
		)
	}
	switch input.Kind {
	case executionstore.ModelWorkStart:
		if input.ModelCallContextID != uuid.Nil ||
			input.SourceModelCallContextID != uuid.Nil ||
			input.SourceModelOutputID != uuid.Nil {
			return errors.New("start model work cannot have context or output identity")
		}
	case executionstore.ModelWorkResume:
		if input.ModelCallContextID == uuid.Nil ||
			input.SourceModelCallContextID != uuid.Nil ||
			input.SourceModelOutputID != uuid.Nil {
			return errors.New("resume model work requires only its active context")
		}
	case executionstore.ModelWorkContinue:
		if input.ModelCallContextID != uuid.Nil ||
			input.SourceModelCallContextID == uuid.Nil ||
			input.SourceModelOutputID == uuid.Nil {
			return errors.New("continue model work requires its source context and source output")
		}
	default:
		return fmt.Errorf("unsupported model work kind %q", input.Kind)
	}
	return nil
}
