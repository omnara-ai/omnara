package kernel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/mcp"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type AgentExecutor struct {
	Store                *storage.Store
	ContextBuilder       modelcontext.Builder
	ModelResolver        model.Resolver
	MCP                  mcp.Client
	MCPAuthHTTPClient    *http.Client
	SigV4CredentialCache *mcp.SigV4CredentialCache
	ToolExecutor         tools.Executor
	Now                  func() time.Time

	StreamPublisher notifications.AgentStreamDeltaPublisher
	StreamLog       *slog.Logger

	MCPInitializationBackoff func(attempt int) time.Duration
	ModelRetryDelay          func(time.Duration) time.Duration
}

func (e AgentExecutor) ExecuteModelWork(ctx context.Context, input ModelWorkExecution) (err error) {
	if e.Store == nil {
		return errors.New("kernel store is required")
	}
	if err := validateModelWorkExecution(input); err != nil {
		return err
	}
	if input.Now.IsZero() {
		input.Now = e.now()
	}
	builder := e.contextBuilder()
	toolExecutor := e.configuredToolExecutor()
	modelProducedResponse := false
	defer func() {
		if !shouldPostIntegrationRuntimeMessage(ctx, err, modelProducedResponse) {
			return
		}
		postCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = toolExecutor.PostIntegrationRuntimeMessage(
			postCtx,
			toToolTurn(input),
			integrationRuntimeErrorMessage(err),
		)
	}()
	if e.ModelResolver == nil {
		return errors.New("kernel model resolver is required")
	}

	if input.Kind == executionstore.ModelWorkResume {
		contextRow, found, loadErr := e.Store.Execution().GetModelCallContext(
			ctx,
			input.ProjectID,
			input.AgentID,
			input.ModelCallContextID,
		)
		if loadErr != nil {
			return loadErr
		}
		if !found {
			return errors.New("resume model context is missing")
		}
		if contextRow.OperationKind == executionstore.ModelCallOperationCompaction {
			return e.resumeCompactionContext(
				ctx,
				input,
				builder,
				e.ModelResolver,
				contextRow,
			)
		}
	}

	step, err := e.executeModelStep(ctx, input, builder, e.ModelResolver)
	if err != nil {
		return err
	}
	switch step.State {
	case modelStepWaiting, modelStepDone:
		return nil
	case modelStepToolUse:
		modelProducedResponse = true
		if _, err := e.recordToolCallSourceEvent(
			ctx,
			input,
			step.Context,
			step.Response.ProviderRequestID,
			step.Envelope,
			step.Bundle.ToolSpecs,
			step.StreamedToolCallIDs,
		); err != nil {
			return fmt.Errorf("record tool-call source event: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported model step state %q", step.State)
	}
}

func validateModelWorkExecution(input ModelWorkExecution) error {
	if input.OrgID == storage.NilID ||
		input.ProjectID == storage.NilID ||
		input.AgentID == storage.NilID ||
		input.TurnID == storage.NilID ||
		input.RuntimeLockID == storage.NilID ||
		len(input.InputIDs) == 0 ||
		input.OpeningEventSequence <= 0 {
		return errors.New(
			"model work organization, project, agent, turn, runtime lock, opening inputs, and opening event sequence are required",
		)
	}
	switch input.Kind {
	case executionstore.ModelWorkStart:
		if input.ModelCallContextID != storage.NilID ||
			input.SourceModelCallContextID != storage.NilID ||
			input.SourceModelOutputID != storage.NilID {
			return errors.New("start model work cannot have context or output identity")
		}
	case executionstore.ModelWorkResume:
		if input.ModelCallContextID == storage.NilID ||
			input.SourceModelCallContextID != storage.NilID ||
			input.SourceModelOutputID != storage.NilID {
			return errors.New("resume model work requires only its active context")
		}
	case executionstore.ModelWorkContinue:
		if input.ModelCallContextID != storage.NilID ||
			input.SourceModelCallContextID == storage.NilID ||
			input.SourceModelOutputID == storage.NilID {
			return errors.New("continue model work requires its source context and source output")
		}
	default:
		return fmt.Errorf("unsupported model work kind %q", input.Kind)
	}
	return nil
}

func shouldPostIntegrationRuntimeMessage(ctx context.Context, err error, modelProducedResponse bool) bool {
	if modelProducedResponse && !errors.Is(err, storeerr.ErrModelGrantUnavailable) {
		return false
	}
	return shouldPostIntegrationRuntimeError(ctx, err)
}

func shouldPostIntegrationRuntimeError(ctx context.Context, err error) bool {
	if err == nil {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	if errors.Is(err, storeerr.ErrModelGrantUnavailable) {
		return true
	}
	if errors.Is(err, context.Canceled) ||
		errors.Is(err, storeerr.ErrAgentNotAdvanceable) ||
		errors.Is(err, storeerr.ErrDaemonRuntimeUnregistered) ||
		errors.Is(err, storeerr.ErrRuntimeLockInactive) ||
		errors.Is(err, storeerr.ErrStateTransitionConflict) {
		return false
	}
	return true
}

func integrationRuntimeErrorMessage(err error) string {
	if errors.Is(err, storeerr.ErrModelGrantUnavailable) {
		return "I couldn't continue because this project does not have access to the configured model. Ask an admin to grant access or update the agent configured model selection."
	}
	return "I couldn't complete this request because the agent hit a runtime error. Please check the agent logs for details."
}
