package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	asyncToolCallTimeout               = 5 * time.Minute
	asyncToolCompletionTimeout         = 30 * time.Second
	asyncToolPersistenceInitialBackoff = 25 * time.Millisecond
	asyncToolPersistenceMaxBackoff     = time.Second
	backgroundExecutionTimeout         = 5 * time.Minute
)

func (e Executor) dispatchToolHandler(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
	toolCallID storage.ID,
	handler toolHandler,
) (toolDispatchResult, error) {
	if handler.Transactional == nil && handler.Async == nil {
		return nil, errors.New(
			"tool handler requires a transactional or async phase",
		)
	}
	var reservation *AsyncExecutionReservation
	var err error
	if handler.Async != nil {
		reservation, err = ReserveAsyncExecution(ctx)
		if err != nil {
			return nil, err
		}
	}
	defer func() {
		if reservation != nil {
			reservation.Done(nil)
		}
	}()
	var phaseResult transactionalPhaseResult
	execution, err := e.Store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     turn.ProjectID,
			AgentID:       turn.AgentID,
			ToolCallID:    toolCallID,
			RuntimeLockID: turn.RuntimeLockID,
		},
		func(reader *executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			phaseResult = nil
			if handler.Transactional != nil {
				var phaseErr error
				phaseResult, phaseErr = invokeTransactionalToolHandler(
					ctx,
					handler.Transactional,
					transactionalToolContext{
						Reader: reader,
						Turn:   turn,
						Call:   call,
					},
				)
				if phaseErr != nil {
					return nil, phaseErr
				}
			} else {
				phaseResult = continueAsync()
			}
			switch result := phaseResult.(type) {
			case completeTransaction:
				completion, err := successfulToolCallCompletion(result.content)
				if err != nil {
					return nil, err
				}
				return executionstore.CompleteToolCallInExecution(completion), nil
			case continueAsyncTransaction:
				if handler.Async == nil {
					return nil, rollbackInvalidTransactionResult(errors.New(
						"transactional tool phase requested Async, but no Async handler is registered",
					))
				}
				return executionstore.StartToolCallAsync(), nil
			case executeCommandTransaction:
				if result.command == nil {
					return nil, rollbackInvalidTransactionResult(errors.New(
						"transactional tool command is required",
					))
				}
				return result.command, nil
			case failTransaction:
				if result.cause == nil {
					return nil, rollbackInvalidTransactionResult(
						errors.New("transactional tool failure requires a cause"),
					)
				}
				return nil, rollbackToolTransactionError{failure: result}
			case nil:
				return nil, rollbackInvalidTransactionResult(
					errors.New("transactional tool phase returned no result"),
				)
			default:
				return nil, rollbackInvalidTransactionResult(fmt.Errorf(
					"unsupported transactional tool phase result %T",
					phaseResult,
				))
			}
		},
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrToolCallInProgress) {
			return toolDispatchAwaiting{}, nil
		}
		if errors.Is(err, storeerr.ErrInvalidToolCallDisposition) {
			return toolDispatchFailed{cause: err}, nil
		}
		var rollback rollbackToolTransactionError
		if errors.As(err, &rollback) {
			return toolDispatchFailed{
				content: rollback.failure.content,
				cause:   rollback.failure.cause,
			}, nil
		}
		if command, ok := phaseResult.(executeCommandTransaction); ok && command.onError != nil {
			mapped, mapErr := command.onError(err)
			if mapErr != nil {
				return nil, mapErr
			}
			switch failure := mapped.(type) {
			case failTransaction:
				return toolDispatchFailed(failure), nil
			case nil:
			default:
				return nil, fmt.Errorf(
					"unsupported tool command error result %T",
					mapped,
				)
			}
		}
		return nil, err
	}
	pipeline := toolPhasePipeline{
		executor:   e,
		turn:       turn,
		call:       call,
		handler:    handler,
		toolCallID: toolCallID,
	}
	result, asyncStarted, err := pipeline.advanceAfterTransaction(
		ctx,
		phaseResult,
		execution,
		reservation,
	)
	if asyncStarted {
		reservation = nil
	}
	return result, err
}

type toolPhasePipeline struct {
	executor   Executor
	turn       Turn
	call       model.ToolCall
	handler    toolHandler
	toolCallID storage.ID
}

func (p toolPhasePipeline) advanceAfterTransaction(
	ctx context.Context,
	result transactionalPhaseResult,
	execution executionstore.ExecuteToolCallResult,
	reservation *AsyncExecutionReservation,
) (toolDispatchResult, bool, error) {
	switch execution.Disposition {
	case executionstore.ToolCallDispositionWaiting:
		if execution.Applied &&
			transactionResultStartsBackground(result) {
			p.executor.submitBackgroundTool(
				p.turn,
				p.call,
				p.handler.Background,
				p.toolCallID,
			)
		}
		return toolDispatchAwaiting{}, false, nil
	case executionstore.ToolCallDispositionCompleted:
		if execution.Applied &&
			transactionResultStartsBackground(result) {
			p.executor.submitBackgroundTool(
				p.turn,
				p.call,
				p.handler.Background,
				p.toolCallID,
			)
		}
		return toolDispatchCompleted{}, false, nil
	case executionstore.ToolCallDispositionRunning:
	default:
		return nil, false, fmt.Errorf(
			"unsupported tool call transaction disposition %d",
			execution.Disposition,
		)
	}
	if p.handler.Async == nil {
		return nil, false, errors.New(
			"tool transaction retained runtime ownership without an Async handler",
		)
	}
	p.executor.startAsyncTool(
		ctx,
		asyncToolContext{
			Executor:   p.executor,
			Turn:       p.turn,
			Call:       p.call,
			ToolCallID: p.toolCallID,
		},
		p.handler,
		reservation,
	)
	return toolDispatchAwaiting{}, true, nil
}

func transactionResultStartsBackground(result transactionalPhaseResult) bool {
	switch result := result.(type) {
	case completeTransaction:
		return true
	case executeCommandTransaction:
		return result.startsBackground
	default:
		return false
	}
}

func (e Executor) submitBackgroundTool(
	turn Turn,
	call model.ToolCall,
	handler backgroundToolHandler,
	toolCallID storage.ID,
) {
	if handler == nil || e.BackgroundRunner == nil {
		return
	}
	e.BackgroundRunner.Submit(call.Name, func(ctx context.Context) error {
		executionCtx, cancel := context.WithTimeout(ctx, backgroundExecutionTimeout)
		defer cancel()
		return handler(executionCtx, backgroundToolContext{
			Executor:   e,
			Turn:       turn,
			Call:       call,
			ToolCallID: toolCallID,
		})
	})
}

func successfulToolCallCompletion(
	content toolResultContent,
) (executionstore.ToolCallCompletionInput, error) {
	contentParts, err := content.contentParts()
	if err != nil {
		return executionstore.ToolCallCompletionInput{}, fmt.Errorf(
			"marshal transactional tool result content parts: %w",
			err,
		)
	}
	return executionstore.ToolCallCompletionInput{
		Outcome:            executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: contentParts,
	}, nil
}

func invokeTransactionalToolHandler(
	ctx context.Context,
	handler transactionalToolHandler,
	call transactionalToolContext,
) (result transactionalPhaseResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			cause := fmt.Errorf(
				"transactional tool %q panicked: %v",
				call.Call.Name,
				recovered,
			)
			logpkg.LoggerFromContext(ctx).Error(
				"transactional tool handler panicked",
				"tool",
				call.Call.Name,
				"error",
				cause,
			)
			result = failInTransaction(toolResultContent{}, cause)
			err = nil
		}
	}()
	return handler(ctx, call)
}

func (e Executor) persistAsyncToolFailure(
	ctx context.Context,
	call asyncToolContext,
	cause error,
) error {
	completionCtx, cancelCompletion := context.WithTimeout(
		context.WithoutCancel(ctx),
		asyncToolCompletionTimeout,
	)
	defer cancelCompletion()
	return e.completeAsyncToolFailure(
		completionCtx,
		call.Turn,
		call.ToolCallID,
		toolResultContent{},
		cause,
	)
}

func (e Executor) requeueAsyncTool(
	ctx context.Context,
	call asyncToolContext,
) error {
	completionCtx, cancelCompletion := context.WithTimeout(
		context.WithoutCancel(ctx),
		asyncToolCompletionTimeout,
	)
	defer cancelCompletion()
	return retryAsyncToolPersistence(completionCtx, func(ctx context.Context) error {
		return e.Store.Execution().RequeueRuntimeToolCall(
			ctx,
			executionstore.RequeueRuntimeToolCallInput{
				ProjectID:     call.Turn.ProjectID,
				AgentID:       call.Turn.AgentID,
				ToolCallID:    call.ToolCallID,
				RuntimeLockID: call.Turn.RuntimeLockID,
			},
		)
	})
}

func retryAsyncToolPersistence(
	ctx context.Context,
	persist func(context.Context) error,
) error {
	backoff := asyncToolPersistenceInitialBackoff
	for {
		err := persist(ctx)
		if err == nil || !retryableAsyncToolPersistenceError(ctx, err) {
			return err
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return errors.Join(err, ctx.Err())
		case <-timer.C:
		}
		backoff = min(backoff*2, asyncToolPersistenceMaxBackoff)
	}
}

func retryableAsyncToolPersistenceError(ctx context.Context, err error) bool {
	var schemaMismatch *dbconn.SchemaVersionMismatchError
	return ctx.Err() == nil &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded) &&
		!errors.As(err, &schemaMismatch) &&
		!errors.Is(err, storeerr.ErrRuntimeLockInactive) &&
		!errors.Is(err, storeerr.ErrStateTransitionConflict) &&
		!errors.Is(err, storeerr.ErrIdempotencyConflict) &&
		!errors.Is(err, storeerr.ErrInvalidToolCallDisposition) &&
		!errors.Is(err, storeerr.ErrNotFound) &&
		!errors.Is(err, storeerr.ErrInvalidRequest)
}

func (e Executor) startAsyncTool(
	ctx context.Context,
	call asyncToolContext,
	handler toolHandler,
	reservation *AsyncExecutionReservation,
) {
	reservation.Start()
	go func() {
		var completionErr error
		defer func() {
			if recovered := recover(); recovered != nil {
				logpkg.LoggerFromContext(ctx).Error(
					"async tool goroutine cleanup panicked",
					"tool",
					call.Call.Name,
					"error",
					fmt.Sprint(recovered),
				)
			}
		}()
		defer func() {
			reservation.Done(completionErr)
		}()
		defer func() {
			if recovered := recover(); recovered != nil {
				cause := fmt.Errorf(
					"async tool %q panicked: %v",
					call.Call.Name,
					recovered,
				)
				logpkg.LoggerFromContext(ctx).Error(
					"async tool handler panicked",
					"tool",
					call.Call.Name,
					"error",
					cause,
				)
				completionCtx, cancelCompletion := context.WithTimeout(
					context.WithoutCancel(ctx),
					asyncToolCompletionTimeout,
				)
				defer cancelCompletion()
				completionErr = e.completeAsyncToolFailure(
					completionCtx,
					call.Turn,
					call.ToolCallID,
					toolResultContent{},
					cause,
				)
			}
		}()
		completionErr = e.executeAsyncTool(ctx, call, handler)
	}()
}

func (e Executor) executeAsyncTool(
	ctx context.Context,
	call asyncToolContext,
	handler toolHandler,
) error {
	executionCtx, cancelExecution := context.WithTimeout(ctx, asyncToolCallTimeout)
	defer cancelExecution()
	if err := e.Store.Execution().EnsureRuntimeLockActive(
		executionCtx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.Turn.RuntimeLockID,
	); err != nil {
		if !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
			return errors.Join(err, e.requeueAsyncTool(ctx, call))
		}
		return e.persistAsyncToolFailure(ctx, call, err)
	}
	result, runErr := handler.Async(executionCtx, call)
	if runErr != nil {
		return e.persistAsyncToolFailure(ctx, call, runErr)
	}
	completionCtx, cancelCompletion := context.WithTimeout(
		context.WithoutCancel(ctx),
		asyncToolCompletionTimeout,
	)
	defer cancelCompletion()
	switch result := result.(type) {
	case completeAsync:
		if err := e.completeAsyncToolSuccess(
			completionCtx,
			call.Turn,
			call.ToolCallID,
			result.content,
		); err != nil {
			return err
		}
	case awaitDurableAsync:
		if err := retryAsyncToolPersistence(
			completionCtx,
			func(ctx context.Context) error {
				return e.Store.Execution().ReleaseToolCallRuntimeOwnership(
					ctx,
					executionstore.ReleaseToolCallRuntimeOwnershipInput{
						ProjectID:     call.Turn.ProjectID,
						AgentID:       call.Turn.AgentID,
						ToolCallID:    call.ToolCallID,
						RuntimeLockID: call.Turn.RuntimeLockID,
					},
				)
			},
		); err != nil {
			if errors.Is(err, storeerr.ErrInvalidToolCallDisposition) {
				return e.completeAsyncToolFailure(
					completionCtx,
					call.Turn,
					call.ToolCallID,
					toolResultContent{},
					err,
				)
			}
			return err
		}
	case failAsync:
		if result.cause == nil {
			return e.completeAsyncToolFailure(
				completionCtx,
				call.Turn,
				call.ToolCallID,
				toolResultContent{},
				errors.New("async tool failure requires a cause"),
			)
		}
		return e.completeAsyncToolFailure(
			completionCtx,
			call.Turn,
			call.ToolCallID,
			result.content,
			result.cause,
		)
	case nil:
		return e.completeAsyncToolFailure(
			completionCtx,
			call.Turn,
			call.ToolCallID,
			toolResultContent{},
			errors.New("async tool phase returned no result"),
		)
	default:
		return e.completeAsyncToolFailure(
			completionCtx,
			call.Turn,
			call.ToolCallID,
			toolResultContent{},
			fmt.Errorf("unsupported async tool phase result %T", result),
		)
	}
	e.submitBackgroundTool(
		call.Turn,
		call.Call,
		handler.Background,
		call.ToolCallID,
	)
	return nil
}

type toolHandler struct {
	Transactional transactionalToolHandler
	Async         asyncToolHandler
	Background    backgroundToolHandler
}

type transactionalToolContext struct {
	Reader *executionstore.ToolCallReader
	Turn   Turn
	Call   model.ToolCall
}

type asyncToolContext struct {
	Executor   Executor
	Turn       Turn
	Call       model.ToolCall
	ToolCallID storage.ID
}

type backgroundToolContext struct {
	Executor   Executor
	Turn       Turn
	Call       model.ToolCall
	ToolCallID storage.ID
}

type transactionalToolHandler func(
	context.Context,
	transactionalToolContext,
) (transactionalPhaseResult, error)

type asyncToolHandler func(
	context.Context,
	asyncToolContext,
) (asyncPhaseResult, error)

type backgroundToolHandler func(
	context.Context,
	backgroundToolContext,
) error

type transactionalPhaseResult interface {
	transactionalPhaseResult()
}

type completeTransaction struct {
	content toolResultContent
}

type continueAsyncTransaction struct{}

type executeCommandTransaction struct {
	command          executionstore.ToolCallCommand
	onError          func(error) (transactionalPhaseResult, error)
	startsBackground bool
}

type failTransaction struct {
	content toolResultContent
	cause   error
}

func (completeTransaction) transactionalPhaseResult()       {}
func (continueAsyncTransaction) transactionalPhaseResult()  {}
func (executeCommandTransaction) transactionalPhaseResult() {}
func (failTransaction) transactionalPhaseResult()           {}

func completeInTransaction(content toolResultContent) transactionalPhaseResult {
	if !content.isSet {
		return failTransaction{
			cause: errors.New("transactional tool completion requires result content"),
		}
	}
	return completeTransaction{content: content}
}

func continueAsync() transactionalPhaseResult {
	return continueAsyncTransaction{}
}

func executeInTransaction(
	command executionstore.ToolCallCommand,
	onError func(error) (transactionalPhaseResult, error),
) transactionalPhaseResult {
	return executeCommandTransaction{
		command:          command,
		onError:          onError,
		startsBackground: true,
	}
}

func failInTransaction(content toolResultContent, cause error) transactionalPhaseResult {
	return failTransaction{content: content, cause: cause}
}

type asyncPhaseResult interface {
	asyncPhaseResult()
}

type completeAsync struct {
	content toolResultContent
}

type awaitDurableAsync struct{}

type failAsync struct {
	content toolResultContent
	cause   error
}

func (completeAsync) asyncPhaseResult()     {}
func (awaitDurableAsync) asyncPhaseResult() {}
func (failAsync) asyncPhaseResult()         {}

func completeAsynchronously(content toolResultContent) asyncPhaseResult {
	if !content.isSet {
		return failAsync{
			cause: errors.New("async tool completion requires result content"),
		}
	}
	return completeAsync{content: content}
}

func awaitDurableAsynchronously() asyncPhaseResult {
	return awaitDurableAsync{}
}

func failAsynchronously(content toolResultContent, cause error) asyncPhaseResult {
	return failAsync{content: content, cause: cause}
}

type toolDispatchResult interface {
	toolDispatchResult()
}

type toolDispatchAwaiting struct{}
type toolDispatchCompleted struct{}

type toolDispatchFailed struct {
	content toolResultContent
	cause   error
}

func (toolDispatchAwaiting) toolDispatchResult()  {}
func (toolDispatchCompleted) toolDispatchResult() {}
func (toolDispatchFailed) toolDispatchResult()    {}

type rollbackToolTransactionError struct {
	failure failTransaction
}

func rollbackInvalidTransactionResult(cause error) error {
	return rollbackToolTransactionError{
		failure: failTransaction{cause: cause},
	}
}

func (e rollbackToolTransactionError) Error() string {
	return e.failure.cause.Error()
}

func (e rollbackToolTransactionError) Unwrap() error {
	return e.failure.cause
}
