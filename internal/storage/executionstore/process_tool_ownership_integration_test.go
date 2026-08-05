//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestDurableProcessCommandsChooseWaitingDisposition(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("process", func(t *testing.T) {
		fixture := newProcessDaemonFixture(t, ctx, "process_command_waiting")
		toolCallID := createToolCallForProcessTest(
			t,
			ctx,
			fixture,
			"process_command_waiting",
			"run_command",
		)
		execution, err := fixture.Store.Execution().ExecuteToolCall(
			ctx,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallID,
				RuntimeLockID: fixture.Lock.ID,
			},
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.StartProcessForToolCall(executionstore.CreateProcessInput{
					AgentMachineBindingID: fixture.BindingID,
					Command:               "sleep 1",
					ShellSelector:         "sh",
					Cwd:                   "/work",
				}), nil
			},
		)
		if err != nil {
			t.Fatalf("start process command: %v", err)
		}
		if execution.Disposition != executionstore.ToolCallDispositionWaiting || !execution.Applied {
			t.Fatalf("start process execution = %+v, want newly waiting", execution)
		}
		if _, found, err := fixture.Store.Execution().GetProcessByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			toolCallID,
		); err != nil || !found {
			t.Fatalf("started process found=%v err=%v", found, err)
		}
	})

	t.Run("process action", func(t *testing.T) {
		fixture := newProcessDaemonFixture(t, ctx, "action_command_waiting")
		toolCallIDs := createToolCallBatchForProcessTest(
			t,
			ctx,
			fixture,
			"action_command_waiting",
			[]processToolCallBatchItem{
				builtInProcessToolCallBatchItem("action_command_waiting_process", "run_command"),
				builtInProcessToolCallBatchItem("action_command_waiting", "write_process"),
			},
		)
		process, err := startProcessForTest(
			ctx,
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallIDs[0],
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessInput{
				AgentMachineBindingID: fixture.BindingID,
				Command:               "cat",
				ShellSelector:         "sh",
				Cwd:                   "/work",
			},
		)
		if err != nil {
			t.Fatalf("start process: %v", err)
		}
		if _, found, err := acceptDaemonProcessForTest(
			ctx,
			fixture.Store,
			fixture.OrgID,
			fixture.MachineID,
			fixture.RuntimeID,
			process.ID,
		); err != nil || !found {
			t.Fatalf("accept process found=%v err=%v", found, err)
		}
		execution, err := fixture.Store.Execution().ExecuteToolCall(
			ctx,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallIDs[1],
				RuntimeLockID: fixture.Lock.ID,
			},
			func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
				return executionstore.CreateProcessActionForToolCall(
					executionstore.CreateProcessActionInput{
						ProcessID:  process.ID,
						ActionKind: executionstore.ProcessActionKindWrite,
						Payload:    json.RawMessage(`{"data":"hello"}`),
					},
				), nil
			},
		)
		if err != nil {
			t.Fatalf("create process action command: %v", err)
		}
		if execution.Disposition != executionstore.ToolCallDispositionWaiting || !execution.Applied {
			t.Fatalf("process action execution = %+v, want newly waiting", execution)
		}
		if _, found, err := fixture.Store.Execution().GetProcessActionByToolCall(
			ctx,
			testProjectID,
			fixture.AgentID,
			toolCallIDs[1],
		); err != nil || !found {
			t.Fatalf("created process action found=%v err=%v", found, err)
		}
	})
}
