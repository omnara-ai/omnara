package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func isStorageExhaustion(err error) bool {
	return errors.Is(err, syscall.ENOSPC) ||
		errors.Is(err, syscall.EDQUOT) ||
		errors.Is(err, statedb.ErrFull) ||
		errors.Is(err, errRunnerStorageExhaustion)
}

func sendStorageExhaustionReport(
	ctx context.Context,
	transport daemonReportTransport,
	processID string,
) error {
	body, err := json.Marshal(daemonReportedEvent{
		Type:               daemonprotocol.EventProcessFinished,
		ProcessID:          processID,
		State:              daemonprotocol.ProcessStateFailed,
		StateReasonCode:    daemonprotocol.ProcessReasonMachineStorageExhausted,
		StateReasonMessage: daemonprotocol.ProcessMessageMachineStorageExhausted,
	})
	if err != nil {
		return err
	}
	ack, err := transport.SendReport(ctx, statedb.Report{
		ID:        uuid.NewString(),
		ProcessID: processID,
		Kind:      statedb.ReportProcessTerminal,
		Body:      body,
	})
	if err != nil {
		return err
	}
	if ack.status == daemonprotocol.AckStatusCommitted ||
		ack.status == daemonprotocol.AckStatusCleanupOnly {
		return nil
	}
	return fmt.Errorf(
		"storage exhaustion report rejected (%s, %s): %s",
		ack.status,
		ack.code,
		ack.err,
	)
}

func (c *Client) handleAcceptedStorageFailure(
	ctx context.Context,
	transport daemonReportTransport,
	runtime *processRuntime,
	cause error,
) (bool, error) {
	ready := errors.Is(cause, errStorageExhaustionTerminalReady)
	if !ready && !isStorageExhaustion(cause) {
		return false, cause
	}
	if runtime == nil || runtime.runner == nil {
		return false, errors.New("storage-exhausted process has no supervisor")
	}
	runner := runtime.runner
	c.processMu.Lock()
	current := c.processes[runtime.processID]
	if runtime.storageFailureReporting || (current != nil && current != runtime) {
		c.processMu.Unlock()
		return true, nil
	}
	runtime.storageFailureReporting = true
	if current == nil {
		c.processes[runtime.processID] = runtime
	}
	c.processMu.Unlock()
	if !ready {
		terminateCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			15*time.Second,
		)
		err := runner.Terminate(
			terminateCtx,
			daemonprotocol.ProcessReasonMachineStorageExhausted,
		)
		cancel()
		if err != nil && !errors.Is(err, errStorageExhaustionTerminalReady) {
			c.processMu.Lock()
			runtime.storageFailureReporting = false
			c.processMu.Unlock()
			return false, err
		}
	}

	err := sendStorageExhaustionReport(ctx, transport, runtime.processID)
	if err != nil {
		c.processMu.Lock()
		runtime.storageFailureReporting = false
		c.processMu.Unlock()
		return true, err
	}
	if !runner.IsDone() {
		terminateCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			10*time.Second,
		)
		err = runner.Terminate(terminateCtx, "server_resolved")
		cancel()
	}
	c.processMu.Lock()
	if err != nil {
		runtime.storageFailureReporting = false
	} else if c.processes[runtime.processID] == runtime {
		c.processes[runtime.processID] = &processRuntime{
			processID:            runtime.processID,
			supervisorInstanceID: runtime.supervisorInstanceID,
			cleanupOnly:          true,
		}
	}
	c.processMu.Unlock()
	if err == nil {
		if socket, ok := transport.(*daemonSocketTransport); ok {
			socket.forgetResolvedProcessActions(runtime.processID)
		}
	}
	return true, err
}

func (c *Client) storageFailureRuntimes() []*processRuntime {
	c.processMu.RLock()
	defer c.processMu.RUnlock()
	var runtimes []*processRuntime
	for _, runtime := range c.processes {
		if runtime == nil || runtime.cleanupOnly || runtime.storageFailureReporting {
			continue
		}
		runner, ok := runtime.runner.(*ipcProcessRunner)
		if ok && runner.storageExhaustionReady() {
			runtimes = append(runtimes, runtime)
		}
	}
	return runtimes
}
