//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

type backgroundRunnerFunc func(string, func(context.Context) error) bool

func (f backgroundRunnerFunc) Submit(label string, task func(context.Context) error) bool {
	return f(label, task)
}

func (f backgroundRunnerFunc) TrySubmit(label string, task func(context.Context) error) bool {
	return f(label, task)
}

func dispatchTestStructuredResult(raw string) toolResultContent {
	content, err := structuredToolResultContent(json.RawMessage(raw))
	if err != nil {
		panic(err)
	}
	return content
}

func TestTransactionalToolDispatchUsesOneDatabaseConnection(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "single-connection-transaction")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_single_connection_transaction",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)

	config := fixture.Pool.Config()
	config.MinConns = 0
	config.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open single-connection pool: %v", err)
	}
	t.Cleanup(pool.Close)

	turn := fixture.turn()
	turn.Tools = map[string]ToolSpec{
		"list_processes": {
			Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
		},
	}
	result, err := (Executor{
		Store: storage.NewStore(pool),
		Now:   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}).Dispatch(ctx, turn, call)
	if err != nil {
		t.Fatalf("dispatch transactional tool with one database connection: %v", err)
	}
	if result.Disposition != DispatchCompleted {
		t.Fatalf("transactional list_processes disposition = %d, want completed", result.Disposition)
	}
}

func TestStopProcessDispatchPreservesTerminalResults(t *testing.T) {
	tests := []struct {
		name            string
		mode            string
		processState    string
		wantOutcome     string
		wantResultKey   string
		wantResultValue string
	}{
		{
			name:            "already stopped",
			mode:            "terminate",
			processState:    "exited",
			wantOutcome:     "succeeded",
			wantResultKey:   "state_reason_code",
			wantResultValue: "already_stopped",
		},
		{
			name:            "unknown state",
			mode:            "interrupt",
			processState:    "unknown",
			wantOutcome:     "failed",
			wantResultKey:   "error_code",
			wantResultValue: "process_state_unknown",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newIntegrationToolFixture(t, ctx, "stop-process-"+test.processState)
			binding := createExecutableBinding(
				t,
				ctx,
				fixture.Store,
				fixture.User.ID,
				"stop-"+test.processState,
				fixture.Now.Add(10*time.Second),
			)
			var agentMachineBindingID storage.ID
			if err := fixture.Pool.QueryRow(
				ctx,
				`INSERT INTO agent_machine_bindings(
				   org_id, project_id, agent_id, machine_id, machine_ref,
				   binding_kind, state, created_at, updated_at
				 )
				 VALUES ($1, $2, $3, $4, 'mchr-stp001', 'explicit', 'attached', $5, $5)
				 RETURNING id`,
				toolsTestOrgID,
				toolsTestProjectID,
				fixture.Agent.ID,
				binding.MachineID,
				fixture.Now.Add(11*time.Second),
			).Scan(&agentMachineBindingID); err != nil {
				t.Fatalf("attach process machine: %v", err)
			}

			processID := toolsTestID("stop-process-" + test.processState)
			processHandle, err := encodeProcessID(processID)
			if err != nil {
				t.Fatalf("encode process id: %v", err)
			}
			runCall := model.ToolCall{
				ID:    "call_run_" + test.processState,
				Name:  "run_command",
				Input: json.RawMessage(`{"command":"true"}`),
			}
			stopCall := model.ToolCall{
				ID:   "call_stop_" + test.processState,
				Name: "stop_process",
				Input: json.RawMessage(
					`{"process_id":"` + processHandle + `","mode":"` + test.mode + `"}`,
				),
			}
			fixture.recordToolCalls(
				t,
				ctx,
				[]model.ToolCall{runCall, stopCall},
				fixture.Now.Add(12*time.Second),
			)

			runToolCallID := fixture.toolCallID(t, ctx, runCall.ID)
			var processLastActivityAtBefore time.Time
			if test.processState == "exited" {
				if err := fixture.Pool.QueryRow(
					ctx,
					`INSERT INTO processes(
					   id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id,
					   agent_machine_binding_id, machine_id, execution_granted_at,
					   io_mode, command, shell_selector, cwd,
					   state, source_started_at, source_ended_at, state_changed_at,
					   exit_code, created_at, updated_at
					 )
						 VALUES (
						   $1, $2, $3, $4, $5, $6, $7, $8, $9,
						   'pipe', 'true', 'sh', '/',
						   'exited', $9, $10, $10, 0, $9, $10
						 )
						 RETURNING last_activity_at`,
					processID,
					toolsTestOrgID,
					toolsTestProjectID,
					fixture.Agent.ID,
					runToolCallID,
					fixture.Lock.ID,
					agentMachineBindingID,
					binding.MachineID,
					fixture.Now.Add(13*time.Second),
					fixture.Now.Add(14*time.Second),
				).Scan(&processLastActivityAtBefore); err != nil {
					t.Fatalf("seed exited process: %v", err)
				}
			} else {
				if _, err := fixture.Pool.Exec(
					ctx,
					`INSERT INTO processes(
					   id, org_id, project_id, agent_id, tool_call_id, runtime_lock_id,
					   agent_machine_binding_id, machine_id, execution_granted_at,
					   io_mode, command, shell_selector, cwd,
					   state, state_reason_code, state_changed_at, created_at, updated_at
					 )
					 VALUES (
					   $1, $2, $3, $4, $5, $6, $7, $8, $9,
					   'pipe', 'true', 'sh', '/',
					   'unknown', 'daemon_lost', $9, $9, $9
					 )`,
					processID,
					toolsTestOrgID,
					toolsTestProjectID,
					fixture.Agent.ID,
					runToolCallID,
					fixture.Lock.ID,
					agentMachineBindingID,
					binding.MachineID,
					fixture.Now.Add(13*time.Second),
				); err != nil {
					t.Fatalf("seed unknown process: %v", err)
				}
			}

			turn := fixture.turn()
			turn.Tools["stop_process"] = ToolSpec{
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			}
			result, err := (Executor{Store: fixture.Store}).Dispatch(ctx, turn, stopCall)
			if err != nil {
				t.Fatalf("dispatch stop_process: %v", err)
			}
			if result.Disposition != DispatchCompleted {
				t.Fatalf("stop_process disposition = %d, want completed", result.Disposition)
			}
			body := toolResultMapFromTestParts(t, result.ContentParts)
			if body[test.wantResultKey] != test.wantResultValue {
				t.Fatalf("stop_process result = %s", result.ContentParts)
			}
			assertDispatchTestToolOutcome(t, ctx, fixture, stopCall.ID, test.wantOutcome)
			var actionCount int
			if err := fixture.Pool.QueryRow(
				ctx,
				`SELECT count(*) FROM process_actions WHERE tool_call_id = $1`,
				fixture.toolCallID(t, ctx, stopCall.ID),
			).Scan(&actionCount); err != nil {
				t.Fatalf("count stop actions: %v", err)
			}
			if actionCount != 0 {
				t.Fatalf("stop actions = %d, want 0", actionCount)
			}
			if test.processState == "exited" {
				var processLastActivityAtAfter time.Time
				if err := fixture.Pool.QueryRow(
					ctx,
					`SELECT last_activity_at FROM processes WHERE id = $1`,
					processID,
				).Scan(&processLastActivityAtAfter); err != nil {
					t.Fatalf("load stopped process activity: %v", err)
				}
				if !processLastActivityAtAfter.After(processLastActivityAtBefore) {
					t.Fatalf(
						"stopped process activity = %s, want after %s",
						processLastActivityAtAfter,
						processLastActivityAtBefore,
					)
				}
			}
		})
	}
}

func TestToolHandlerPhaseOrdering(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "typed-phase-ordering")
	backgroundRunner, err := NewBackgroundExecutionRunner(ctx, nil, 1)
	if err != nil {
		t.Fatalf("new background runner: %v", err)
	}
	defer backgroundRunner.Shutdown()
	executor := Executor{
		Store:            fixture.Store,
		BackgroundRunner: backgroundRunner,
		Now:              func() time.Time { return fixture.Now.Add(30 * time.Second) },
	}
	turn := fixture.turn()

	calls := []model.ToolCall{
		{
			ID:    "call_transactional_phase",
			Name:  "list_processes",
			Input: json.RawMessage(`{}`),
		},
		{
			ID:    "call_async_phase",
			Name:  "list_processes",
			Input: json.RawMessage(`{}`),
		},
		{
			ID:    "call_combined_phases",
			Name:  "list_processes",
			Input: json.RawMessage(`{}`),
		},
	}
	fixture.recordToolCalls(t, ctx, calls, fixture.Now.Add(21*time.Second))

	transactional := calls[0]
	result, err := executor.dispatchToolHandler(
		ctx,
		turn,
		transactional,
		fixture.toolCallID(t, ctx, transactional.ID),
		toolHandler{Transactional: func(
			context.Context,
			transactionalToolContext,
		) (transactionalPhaseResult, error) {
			return completeInTransaction(
				dispatchTestStructuredResult(`{"value":"transactional"}`),
			), nil
		}},
	)
	if err != nil {
		t.Fatalf("transactional-only dispatch: %v", err)
	}
	if _, ok := result.(toolDispatchCompleted); !ok {
		t.Fatalf("transactional dispatch result = %T, want completed", result)
	}
	assertDispatchTestToolState(t, ctx, fixture, transactional.ID, "completed", false)
	assertDispatchTestInteractionCount(t, ctx, fixture, transactional.ID, 0)
	assertDispatchTestResultCount(t, ctx, fixture, transactional.ID, 1)

	asyncOnly := calls[1]
	asyncOnlyID := fixture.toolCallID(t, ctx, asyncOnly.ID)
	dispatchTestAsyncHandler(
		t,
		ctx,
		executor,
		turn,
		asyncOnly,
		asyncOnlyID,
		toolHandler{Async: func(
			ctx context.Context,
			call asyncToolContext,
		) (asyncPhaseResult, error) {
			if call.ToolCallID != asyncOnlyID {
				return nil, errors.New("async handler received the wrong tool call")
			}
			var committed bool
			if err := fixture.Pool.QueryRow(
				ctx,
				`SELECT state = 'running' AND runtime_lock_id = $2
				 FROM tool_calls
				 WHERE id = $1`,
				call.ToolCallID,
				call.Turn.RuntimeLockID,
			).Scan(&committed); err != nil {
				return nil, err
			}
			if !committed {
				return nil, errors.New("async handler started before its claim committed")
			}
			return completeAsynchronously(
				dispatchTestStructuredResult(`{"value":"async"}`),
			), nil
		}},
	)
	assertDispatchTestToolState(t, ctx, fixture, asyncOnly.ID, "completed", false)

	combined := calls[2]
	backgroundStarted := make(chan struct{})
	dispatchTestAsyncHandler(
		t,
		ctx,
		executor,
		turn,
		combined,
		fixture.toolCallID(t, ctx, combined.ID),
		toolHandler{
			Transactional: func(
				_ context.Context,
				call transactionalToolContext,
			) (transactionalPhaseResult, error) {
				return createDispatchTestInteraction(call)
			},
			Async: func(
				ctx context.Context,
				call asyncToolContext,
			) (asyncPhaseResult, error) {
				var interactions int
				if err := fixture.Pool.QueryRow(
					ctx,
					`SELECT count(*) FROM agent_interactions WHERE tool_call_id = $1`,
					call.ToolCallID,
				).Scan(&interactions); err != nil {
					return nil, err
				}
				if interactions != 1 {
					return nil, errors.New(
						"transactional phase was not committed before async execution",
					)
				}
				return awaitDurableAsynchronously(), nil
			},
			Background: func(
				ctx context.Context,
				call backgroundToolContext,
			) error {
				var released bool
				if err := fixture.Pool.QueryRow(
					ctx,
					`SELECT state = 'waiting' AND runtime_lock_id IS NULL
					 FROM tool_calls
					 WHERE id = $1`,
					call.ToolCallID,
				).Scan(&released); err != nil {
					return err
				}
				if !released {
					return errors.New("background started before durable handoff committed")
				}
				if call.Turn.RuntimeLockID == storage.NilID {
					return errors.New("background lost dispatch context")
				}
				close(backgroundStarted)
				return nil
			},
		},
	)
	select {
	case <-backgroundStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("background phase did not start after async durable handoff")
	}
	assertDispatchTestToolState(t, ctx, fixture, combined.ID, "waiting", false)
	assertDispatchTestInteractionCount(t, ctx, fixture, combined.ID, 1)
}

func TestCompletedAsyncAdmitsBackgroundBeforeReleasingCapacity(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fixture := newIntegrationToolFixture(t, ctx, "completed-async-background")
	admissionStarted := make(chan struct{})
	admit := make(chan struct{})
	runner := backgroundRunnerFunc(func(string, func(context.Context) error) bool {
		close(admissionStarted)
		select {
		case <-admit:
			return true
		case <-ctx.Done():
			return false
		}
	})
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_completed_async_background",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	scope := NewAsyncExecutionScope(NewAsyncExecutionLimiter(1))
	result, err := (Executor{
		Store:            fixture.Store,
		BackgroundRunner: runner,
		Now:              func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}).dispatchToolHandler(
		WithAsyncExecutionScope(ctx, scope),
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{
			Async: func(
				context.Context,
				asyncToolContext,
			) (asyncPhaseResult, error) {
				return completeAsynchronously(
					dispatchTestStructuredResult(`{"value":"async"}`),
				), nil
			},
			Background: func(
				context.Context,
				backgroundToolContext,
			) error {
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("start async dispatch: %v", err)
	}
	if _, ok := result.(toolDispatchAwaiting); !ok {
		t.Fatalf("async dispatch result = %T, want awaiting", result)
	}
	scope.Seal()

	select {
	case <-admissionStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("completed async phase did not attempt background admission")
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "completed", false)
	select {
	case <-scope.Done():
		t.Fatal("async capacity was released while background admission was blocked")
	default:
	}

	close(admit)
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("async capacity was not released after background admission")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("async completion: %v", err)
	}
}

func TestTransactionalCompletionSkipsAsync(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "transactional-completion")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_transactional_completion",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	asyncStarted := make(chan struct{}, 1)
	result, err := (Executor{Store: fixture.Store}).dispatchToolHandler(
		ctx,
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{
			Transactional: func(
				context.Context,
				transactionalToolContext,
			) (transactionalPhaseResult, error) {
				return completeInTransaction(
					dispatchTestStructuredResult(`{"value":"transactional"}`),
				), nil
			},
			Async: func(
				context.Context,
				asyncToolContext,
			) (asyncPhaseResult, error) {
				asyncStarted <- struct{}{}
				return completeAsynchronously(newToolResultContent()), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if _, ok := result.(toolDispatchCompleted); !ok {
		t.Fatalf("dispatch result = %T, want completed", result)
	}
	select {
	case <-asyncStarted:
		t.Fatal("async handler started after transactional completion")
	default:
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "completed", false)
	assertDispatchTestInteractionCount(t, ctx, fixture, call.ID, 0)
	assertDispatchTestResultCount(t, ctx, fixture, call.ID, 1)
}

func TestTransactionalFailureRollsBackAndSkipsLaterPhases(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "transactional-failure")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_transactional_failure",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	phaseErr := errors.New("transactional phase rejected")
	asyncStarted := make(chan struct{}, 1)
	backgroundStarted := make(chan struct{}, 1)
	backgroundRunner := backgroundRunnerFunc(func(
		_ string,
		task func(context.Context) error,
	) bool {
		_ = task(ctx)
		return true
	})
	result, err := (Executor{
		Store:            fixture.Store,
		BackgroundRunner: backgroundRunner,
	}).dispatchToolHandler(
		ctx,
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{
			Transactional: func(
				context.Context,
				transactionalToolContext,
			) (transactionalPhaseResult, error) {
				return failInTransaction(
					dispatchTestStructuredResult(`{"code":"rejected"}`),
					phaseErr,
				), nil
			},
			Async: func(
				context.Context,
				asyncToolContext,
			) (asyncPhaseResult, error) {
				asyncStarted <- struct{}{}
				return completeAsynchronously(newToolResultContent()), nil
			},
			Background: func(
				context.Context,
				backgroundToolContext,
			) error {
				backgroundStarted <- struct{}{}
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("dispatch failed result: %v", err)
	}
	failed, ok := result.(toolDispatchFailed)
	if !ok || !errors.Is(failed.cause, phaseErr) {
		t.Fatalf("dispatch result = %#v, want rolled-back failure", result)
	}
	failedContent, err := failed.content.contentParts()
	if err != nil {
		t.Fatalf("marshal rolled-back failure: %v", err)
	}
	if string(failedContent) !=
		`[{"type":"structured_data","value":{"code":"rejected"}}]` {
		t.Fatalf("rolled-back failure content = %s", failedContent)
	}
	select {
	case <-asyncStarted:
		t.Fatal("async handler started after transactional failure")
	default:
	}
	select {
	case <-backgroundStarted:
		t.Fatal("background handler started after transactional failure")
	default:
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "ready", false)
	assertDispatchTestInteractionCount(t, ctx, fixture, call.ID, 0)
	assertDispatchTestResultCount(t, ctx, fixture, call.ID, 0)
}

func TestTransactionalPanicRollsBackAndReleasesReservedAsyncCapacity(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "transactional-panic")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_transactional_panic",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	scope := NewAsyncExecutionScope(NewAsyncExecutionLimiter(1))
	scopedCtx := WithAsyncExecutionScope(ctx, scope)
	result, err := (Executor{Store: fixture.Store}).dispatchToolHandler(
		scopedCtx,
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{
			Transactional: func(
				context.Context,
				transactionalToolContext,
			) (transactionalPhaseResult, error) {
				panic("transaction exploded")
			},
			Async: func(
				context.Context,
				asyncToolContext,
			) (asyncPhaseResult, error) {
				return completeAsynchronously(newToolResultContent()), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("dispatch transactional panic: %v", err)
	}
	failed, ok := result.(toolDispatchFailed)
	if !ok || failed.cause == nil ||
		failed.cause.Error() != `transactional tool "list_processes" panicked: transaction exploded` {
		t.Fatalf("dispatch result = %#v, want transactional panic failure", result)
	}
	if scope.Started() {
		t.Fatal("transactional panic started the async phase")
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "ready", false)
	assertDispatchTestInteractionCount(t, ctx, fixture, call.ID, 0)
	assertDispatchTestResultCount(t, ctx, fixture, call.ID, 0)

	reserveCtx, cancelReserve := context.WithTimeout(scopedCtx, time.Second)
	defer cancelReserve()
	reservation, err := ReserveAsyncExecution(reserveCtx)
	if err != nil {
		t.Fatalf("reserve async capacity after transactional panic: %v", err)
	}
	reservation.Done(nil)
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(time.Second):
		t.Fatal("transactional panic leaked its async capacity reservation")
	}
}

func TestUnownedAsyncHandoffFailsOnce(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "unowned-async-handoff")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_unowned_async_handoff",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	if _, err := fixture.Store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       fixture.Agent.ID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		},
	); err != nil {
		t.Fatalf("start async execution: %v", err)
	}

	err := (Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}).executeAsyncTool(
		ctx,
		asyncToolContext{
			Executor:   Executor{Store: fixture.Store},
			Turn:       fixture.turn(),
			Call:       call,
			ToolCallID: toolCallID,
		},
		toolHandler{
			Async: func(
				context.Context,
				asyncToolContext,
			) (asyncPhaseResult, error) {
				return awaitDurableAsynchronously(), nil
			},
		},
	)
	if err != nil {
		t.Fatalf("execute unowned async handoff: %v", err)
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "completed", false)
	assertDispatchTestResultCount(t, ctx, fixture, call.ID, 1)
}

func TestRuntimeToolCanBeRequeuedBeforeAsyncWorkBegins(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "runtime-tool-requeue")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_runtime_tool_requeue",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	toolCallID := fixture.toolCallID(t, ctx, call.ID)
	execution, err := fixture.Store.Execution().ExecuteToolCall(
		ctx,
		executionstore.ExecuteToolCallInput{
			ProjectID:     toolsTestProjectID,
			AgentID:       fixture.Agent.ID,
			ToolCallID:    toolCallID,
			RuntimeLockID: fixture.Lock.ID,
		},
		func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
			return executionstore.StartToolCallAsync(), nil
		},
	)
	if err != nil {
		t.Fatalf("start runtime tool call: %v", err)
	}
	if execution.Disposition != executionstore.ToolCallDispositionRunning ||
		!execution.Applied {
		t.Fatalf("start execution = %+v, want newly running", execution)
	}
	handlerCalled := false
	executor := Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}
	canceledCtx, cancel := context.WithCancel(ctx)
	cancel()
	asyncCall := asyncToolContext{
		Executor:   executor,
		Turn:       fixture.turn(),
		Call:       call,
		ToolCallID: toolCallID,
	}
	err = executor.executeAsyncTool(
		canceledCtx,
		asyncCall,
		toolHandler{Async: func(
			context.Context,
			asyncToolContext,
		) (asyncPhaseResult, error) {
			handlerCalled = true
			return completeAsynchronously(
				dispatchTestStructuredResult(`{"value":"async"}`),
			), nil
		}},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("async preflight error = %v, want canceled context", err)
	}
	if handlerCalled {
		t.Fatal("async handler ran after its runtime preflight failed")
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "ready", false)
	if err := executor.requeueAsyncTool(ctx, asyncCall); err != nil {
		t.Fatalf("repeat async requeue: %v", err)
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "ready", false)
}

func TestAsyncSuccessRemainsAuthoritativeWhenExecutionContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fixture := newIntegrationToolFixture(t, ctx, "async-success-at-cancellation")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_async_success_at_cancellation",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	started := make(chan struct{})
	finish := make(chan struct{})
	scope := NewAsyncExecutionScope(nil)
	result, err := (Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}).dispatchToolHandler(
		WithAsyncExecutionScope(ctx, scope),
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{Async: func(
			context.Context,
			asyncToolContext,
		) (asyncPhaseResult, error) {
			close(started)
			<-finish
			return completeAsynchronously(
				dispatchTestStructuredResult(`{"value":"committed"}`),
			), nil
		}},
	)
	if err != nil {
		t.Fatalf("start async dispatch: %v", err)
	}
	if _, ok := result.(toolDispatchAwaiting); !ok {
		t.Fatalf("initial result = %T, want awaiting", result)
	}
	<-started
	cancel()
	close(finish)
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("async completion did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("persist async completion: %v", err)
	}
	assertDispatchTestToolState(
		t,
		context.Background(),
		fixture,
		call.ID,
		"completed",
		false,
	)
}

func TestAsyncFailureCancelsTransactionalInteraction(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "async-failure-interaction")
	backgroundStarted := make(chan struct{}, 1)
	backgroundRunner := backgroundRunnerFunc(func(
		_ string,
		task func(context.Context) error,
	) bool {
		_ = task(ctx)
		return true
	})
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_async_failure_interaction",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	dispatchTestAsyncHandler(
		t,
		ctx,
		Executor{
			Store:            fixture.Store,
			BackgroundRunner: backgroundRunner,
			Now:              func() time.Time { return fixture.Now.Add(22 * time.Second) },
		},
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{
			Transactional: func(
				_ context.Context,
				call transactionalToolContext,
			) (transactionalPhaseResult, error) {
				return createDispatchTestInteraction(call)
			},
			Async: func(
				context.Context,
				asyncToolContext,
			) (asyncPhaseResult, error) {
				return failAsynchronously(
					dispatchTestStructuredResult(`{"code":"delivery_failed"}`),
					errors.New("delivery failed"),
				), nil
			},
			Background: func(
				context.Context,
				backgroundToolContext,
			) error {
				backgroundStarted <- struct{}{}
				return nil
			},
		},
	)
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "completed", false)
	assertDispatchTestToolOutcome(t, ctx, fixture, call.ID, "failed")
	interaction, found, err := fixture.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.toolCallID(t, ctx, call.ID),
		"question",
	)
	if err != nil {
		t.Fatalf("list interactions after async failure: %v", err)
	}
	if !found || interaction.State != executionstore.AgentInteractionStateCanceled {
		t.Fatalf("interaction after async failure = %+v found=%v, want canceled", interaction, found)
	}
	select {
	case <-backgroundStarted:
		t.Fatal("background phase started after async failure")
	default:
	}
}

func TestAsyncPanicFailsOnlyItsToolAndReleasesCapacity(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixture(t, ctx, "async-panic")
	call := fixture.recordToolCall(
		t,
		ctx,
		"call_async_panic",
		"list_processes",
		`{}`,
		fixture.Now.Add(20*time.Second),
	)
	scope := NewAsyncExecutionScope(NewAsyncExecutionLimiter(1))
	result, err := (Executor{
		Store: fixture.Store,
		Now:   func() time.Time { return fixture.Now.Add(21 * time.Second) },
	}).dispatchToolHandler(
		WithAsyncExecutionScope(ctx, scope),
		fixture.turn(),
		call,
		fixture.toolCallID(t, ctx, call.ID),
		toolHandler{Async: func(
			context.Context,
			asyncToolContext,
		) (asyncPhaseResult, error) {
			panic("provider exploded")
		}},
	)
	if err != nil {
		t.Fatalf("start async dispatch: %v", err)
	}
	if _, ok := result.(toolDispatchAwaiting); !ok {
		t.Fatalf("dispatch result = %T, want awaiting", result)
	}
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("async panic leaked its capacity reservation")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("record panic result: %v", err)
	}
	assertDispatchTestToolState(t, ctx, fixture, call.ID, "completed", false)
	assertDispatchTestToolOutcome(t, ctx, fixture, call.ID, "failed")
	assertDispatchTestResultCount(t, ctx, fixture, call.ID, 1)
}

func createDispatchTestInteraction(
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	value, err := interactionform.New(
		"Question",
		nil,
		[]interactionform.Question{{
			Prompt:  "Continue?",
			Options: []interactionform.Option{{Label: "Yes"}},
		}},
	)
	if err != nil {
		return nil, err
	}
	return executeInTransaction(
		executionstore.CreateQuestionForToolCall(
			executionstore.CreateQuestionInteractionInput{Form: value},
		),
		nil,
	), nil
}

func dispatchTestAsyncHandler(
	t *testing.T,
	ctx context.Context,
	executor Executor,
	turn Turn,
	call model.ToolCall,
	toolCallID storage.ID,
	handler toolHandler,
) {
	t.Helper()
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.dispatchToolHandler(
		WithAsyncExecutionScope(ctx, scope),
		turn,
		call,
		toolCallID,
		handler,
	)
	if err != nil {
		t.Fatalf("start async dispatch: %v", err)
	}
	if _, ok := result.(toolDispatchAwaiting); !ok {
		t.Fatalf("async dispatch result = %T, want awaiting", result)
	}
	scope.Seal()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("async dispatch did not finish")
	}
	if err := scope.Err(); err != nil {
		t.Fatalf("async dispatch: %v", err)
	}
}

func assertDispatchTestToolState(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	providerCallID, wantState string,
	wantRuntimeOwnership bool,
) {
	t.Helper()
	toolCallID := fixture.toolCallID(t, ctx, providerCallID)
	var state string
	var ownsRuntime bool
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT state, coalesce(runtime_lock_id = $2, false)
		 FROM tool_calls
		 WHERE id = $1`,
		toolCallID,
		fixture.Lock.ID,
	).Scan(&state, &ownsRuntime); err != nil {
		t.Fatalf("load tool call state: %v", err)
	}
	if state != wantState || ownsRuntime != wantRuntimeOwnership {
		t.Fatalf(
			"tool call state=%q owns_runtime=%v, want state=%q owns_runtime=%v",
			state,
			ownsRuntime,
			wantState,
			wantRuntimeOwnership,
		)
	}
}

func assertDispatchTestInteractionCount(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	providerCallID string,
	want int,
) {
	t.Helper()
	var count int
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*) FROM agent_interactions WHERE tool_call_id = $1`,
		fixture.toolCallID(t, ctx, providerCallID),
	).Scan(&count); err != nil {
		t.Fatalf("count dispatch test interactions: %v", err)
	}
	if count != want {
		t.Fatalf("dispatch test interactions = %d, want %d", count, want)
	}
}

func assertDispatchTestToolOutcome(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	providerCallID, wantOutcome string,
) {
	t.Helper()
	var outcome string
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT result.outcome
		 FROM tool_call_results result
		 WHERE result.tool_call_id = $1`,
		fixture.toolCallID(t, ctx, providerCallID),
	).Scan(&outcome); err != nil {
		t.Fatalf("load tool call outcome: %v", err)
	}
	if outcome != wantOutcome {
		t.Fatalf("tool call outcome=%q, want %q", outcome, wantOutcome)
	}
}

func assertDispatchTestResultCount(
	t *testing.T,
	ctx context.Context,
	fixture integrationToolFixture,
	providerCallID string,
	want int,
) {
	t.Helper()
	var count int
	if err := fixture.Pool.QueryRow(
		ctx,
		`SELECT count(*)
		 FROM tool_call_results result
		 JOIN tool_call_read_projection call
		   ON call.agent_id = result.agent_id
		  AND call.id = result.tool_call_id
		 WHERE call.project_id = $1 AND result.agent_id = $2 AND result.tool_call_id = $3`,
		toolsTestProjectID,
		fixture.Agent.ID,
		fixture.toolCallID(t, ctx, providerCallID),
	).Scan(&count); err != nil {
		t.Fatalf("count dispatch test results: %v", err)
	}
	if count != want {
		t.Fatalf("dispatch test results = %d, want %d", count, want)
	}
}
