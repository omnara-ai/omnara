//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestAgentProcessLimitSerializesConcurrentStartsAndPreservesReplay(
	t *testing.T,
) {
	t.Parallel()
	ctx := context.Background()
	fixture := newProcessDaemonFixture(t, ctx, "agent_process_limit")

	toolCallItems := make([]processToolCallBatchItem, executionstore.MaxNonTerminalProcessesPerAgent+1)
	for index := range toolCallItems {
		toolCallItems[index] = builtInProcessToolCallBatchItem(
			fmt.Sprintf("agent_process_limit_%d", index),
			"run_command",
		)
	}
	toolCallIDs := createToolCallBatchForProcessTest(
		t,
		ctx,
		fixture,
		"agent_process_limit",
		toolCallItems,
	)
	var err error

	startProcess := func(index int) (executionstore.ProcessRecord, error) {
		return startProcessForTest(
			ctx,
			fixture.Store,
			executionstore.ExecuteToolCallInput{
				ProjectID:     testProjectID,
				AgentID:       fixture.AgentID,
				ToolCallID:    toolCallIDs[index],
				RuntimeLockID: fixture.Lock.ID,
			},
			executionstore.CreateProcessInput{
				AgentMachineBindingID: fixture.BindingID,
				Command:               "sleep 3600",
				ShellSelector:         "sh",
				Cwd:                   "/work",
			},
		)
	}
	processes := make([]executionstore.ProcessRecord, executionstore.MaxNonTerminalProcessesPerAgent-1)
	for index := range processes {
		processes[index], err = startProcess(index)
		if err != nil {
			t.Fatalf("start seed process %d: %v", index, err)
		}
	}

	type startResult struct {
		index   int
		process executionstore.ProcessRecord
		err     error
	}
	results := make(chan startResult, 2)
	start := make(chan struct{})
	var ready sync.WaitGroup
	ready.Add(2)
	for index := executionstore.MaxNonTerminalProcessesPerAgent - 1; index <= executionstore.MaxNonTerminalProcessesPerAgent; index++ {
		go func() {
			ready.Done()
			<-start
			process, startErr := startProcess(index)
			results <- startResult{
				index:   index,
				process: process,
				err:     startErr,
			}
		}()
	}
	ready.Wait()
	close(start)

	var winner, loser startResult
	var started, limited int
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			winner = result
			started++
		case errors.Is(result.err, storeerr.ErrAgentProcessLimitReached):
			loser = result
			limited++
		default:
			t.Fatalf("concurrent start error = %v", result.err)
		}
	}
	if started != 1 || limited != 1 {
		t.Fatalf(
			"concurrent starts succeeded=%d limited=%d, want 1 and 1",
			started,
			limited,
		)
	}

	replayed, err := startProcess(winner.index)
	if err != nil {
		t.Fatalf("replay process at limit: %v", err)
	}
	if replayed.ID != winner.process.ID {
		t.Fatalf(
			"replayed process = %s, want %s",
			replayed.ID,
			winner.process.ID,
		)
	}

	accepted, found, err := acceptDaemonProcessForTest(
		ctx,
		fixture.Store,
		fixture.OrgID,
		fixture.MachineID,
		fixture.RuntimeID,
		processes[0].ID)

	if err != nil || !found {
		t.Fatalf("accept process to release capacity: found=%t err=%v", found, err)
	}
	markProcessStartedForTest(
		t,
		ctx,
		fixture,
		accepted.Process,
		fixture.Now.Add(41*time.Second),
	)
	exitCode := 0
	if _, err := fixture.Store.Execution().CompleteDaemonProcess(
		ctx,
		executionstore.CompleteDaemonProcessInput{
			ProjectID:     testProjectID,
			AgentID:       fixture.AgentID,
			ID:            accepted.Process.ID,
			Authority:     fixture.authority(),
			State:         executionstore.ProcessStateExited,
			ExitCode:      &exitCode,
			SourceEndedAt: fixture.Now.Add(42 * time.Second),
		},
	); err != nil {
		t.Fatalf("complete process to release capacity: %v", err)
	}

	if _, err := startProcess(loser.index); err != nil {
		t.Fatalf("start process after terminal completion: %v", err)
	}
}
