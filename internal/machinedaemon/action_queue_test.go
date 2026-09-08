package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/stretchr/testify/require"
)

func TestStartupProcessesWithoutActionsDoNotCreateQueueWorkers(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	startup := localStartupState{
		Runners: make(map[string]*processRuntime, 1000),
	}
	for i := range 1000 {
		processID := fmt.Sprintf("prc_idle_%d", i)
		startup.Runners[processID] = &processRuntime{processID: processID}
	}
	transport := newDaemonSocketTransport(
		&Client{},
		DaemonRuntime{},
		startup,
	)
	transport.resumeStartupActions(ctx)
	transport.mu.Lock()
	queueCount := len(transport.actionQueues)
	transport.mu.Unlock()
	if queueCount != 0 {
		t.Fatalf("idle startup processes created %d action queues", queueCount)
	}
	transport.stopAndWait(cancel)
}

type frontierTestRunner struct {
	supervisor           *statedb.Supervisor
	processID            string
	supervisorInstanceID string
	done                 chan struct{}
	blocked              chan struct{}
	applied              chan struct{}
	blockOnce            sync.Once
	applyOnce            sync.Once
}

func (r *frontierTestRunner) Status(context.Context) error {
	return nil
}

func (r *frontierTestRunner) StartOnce(context.Context) error {
	return errors.New("unexpected start")
}

func (r *frontierTestRunner) ApplyOnce(
	ctx context.Context,
	action ProcessAction,
) error {
	decision, _, err := r.supervisor.ApplyOnce(ctx, statedb.Action{
		ID:        action.ID,
		ProcessID: r.processID,
		Kind:      action.ActionKind,
		Seq:       action.Seq,
	})
	if err != nil {
		return err
	}
	switch decision {
	case statedb.ApplyBlocked:
		r.blockOnce.Do(func() { close(r.blocked) })
		return statedb.ErrActionBlocked
	case statedb.ApplyExecute:
		body, err := json.Marshal(daemonReportedEvent{
			Type:            "process_action_applied",
			ProcessID:       r.processID,
			ProcessActionID: action.ID,
		})
		if err != nil {
			return err
		}
		if _, err := r.supervisor.FreezeActionReport(
			ctx,
			action.ID,
			statedb.Report{
				ProcessID: r.processID,
				ActionID:  action.ID,
				Kind:      statedb.ReportActionTerminal,
				Body:      body,
			},
		); err != nil {
			return err
		}
		r.applyOnce.Do(func() { close(r.applied) })
		return nil
	case statedb.ApplyAlreadyReported,
		statedb.ApplyAlreadyResolved:
		return nil
	default:
		return errors.New(
			"unexpected local action decision",
		)
	}
}

func (r *frontierTestRunner) CloseUngranted(context.Context) error {
	return errors.New("unexpected close")
}

func (r *frontierTestRunner) Terminate(context.Context, string) error {
	return errors.New("unexpected terminate")
}

func (r *frontierTestRunner) Done() <-chan struct{} {
	return r.done
}

func (r *frontierTestRunner) IsDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func TestActionQueueWaitsForReconciledPredecessorReport(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	const testTimeout = 3 * time.Second

	const (
		installationID       = "ins_action_frontier"
		machineID            = "mch_action_frontier"
		processID            = "prc_action_frontier"
		supervisorInstanceID = "supervisor-instance-action-frontier"
		supervisorToken      = "supervisor-token-action-frontier"
	)
	dbPath := filepath.Join(t.TempDir(), "state.sqlite")
	store, err := statedb.Open(
		ctx,
		dbPath,
		installationID,
		machineID,
	)
	require.NoError(t, err)
	defer store.Close()
	process := statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      supervisorToken,
	}
	require.NoError(t, store.ReserveProcess(ctx, process))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))
	supervisor, err := statedb.OpenSupervisor(
		ctx,
		dbPath,
		installationID,
		machineID,
		processID,
		supervisorInstanceID,
		supervisorToken,
	)
	require.NoError(t, err)
	defer supervisor.Close()
	if execute, err := supervisor.AuthorizeSpawnOnce(
		ctx,
	); err != nil || !execute {
		t.Fatalf("commit execution: execute=%t err=%v", execute, err)
	}
	require.NoError(t, supervisor.RecordSpawned(
		ctx,
		"process_group",
		"123",
	))

	first := statedb.Action{
		ID:        "act_action_frontier_1",
		ProcessID: processID,
		Kind:      "write",
		Seq:       1,
	}
	if decision, _, err := supervisor.ApplyOnce(
		ctx,
		first,
	); err != nil || decision != statedb.ApplyExecute {
		t.Fatalf("apply predecessor: decision=%v err=%v", decision, err)
	}
	firstBody, err := json.Marshal(daemonReportedEvent{
		Type:            "process_action_applied",
		ProcessID:       processID,
		ProcessActionID: first.ID,
	})
	require.NoError(t, err)
	firstReport, err := supervisor.FreezeActionReport(
		ctx,
		first.ID,
		statedb.Report{
			ProcessID: processID,
			ActionID:  first.ID,
			Kind:      statedb.ReportActionTerminal,
			Body:      firstBody,
		},
	)
	require.NoError(t, err)

	runner := &frontierTestRunner{
		supervisor:           supervisor,
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		done:                 make(chan struct{}),
		blocked:              make(chan struct{}),
		applied:              make(chan struct{}),
	}
	client := New(Config{}, nil, nil)
	client.state = store
	client.addProcess(&processRuntime{
		processID:            processID,
		supervisorInstanceID: supervisorInstanceID,
		runner:               runner,
	})
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	defer transport.stopAndWait(cancel)
	next := pendingAction{
		processID: processID,
		enqueued:  true,
		action: ProcessAction{
			ID:         "act_action_frontier_2",
			ActionKind: "write",
			Seq:        2,
		},
	}
	transport.mu.Lock()
	transport.pendingActions[next.action.ID] = next
	transport.mu.Unlock()
	transport.enqueueAcceptedAction(ctx, next)

	select {
	case <-runner.blocked:
	case <-time.After(testTimeout):
		t.Fatal("later action did not wait at the local sequence frontier")
	}
	select {
	case err := <-transport.fatal:
		t.Fatalf("blocked action incorrectly failed the transport: %v", err)
	default:
	}

	require.NoError(t, store.AcknowledgeReport(
		ctx,
		firstReport.ID,
	))
	select {
	case <-runner.applied:
	case <-time.After(testTimeout):
		t.Fatal("later action did not resume after predecessor settlement")
	}
	nextReport, found, err := store.ReportBySlot(
		ctx,
		statedb.ReportActionTerminal,
		processID,
		next.action.ID,
	)
	if err != nil || !found {
		t.Fatalf("later action report: found=%t err=%v", found, err)
	}
	require.NoError(t, store.AcknowledgeReport(
		ctx,
		nextReport.ID,
	))

	transport.workers.Wait()
	transport.mu.Lock()
	_, known := transport.pendingActions[next.action.ID]
	_, queued := transport.actionQueues[processID]
	transport.mu.Unlock()
	if known || queued {
		t.Fatal("settled action retained transport state")
	}
	select {
	case err := <-transport.fatal:
		t.Fatalf("reconciled action queue failed: %v", err)
	default:
	}
}

func TestActionQueueSkipsForgottenAction(t *testing.T) {
	const (
		processID = "prc_forgotten_action"
		actionID  = "act_forgotten_action"
	)
	client := New(Config{}, nil, nil)
	transport := newDaemonSocketTransport(
		&client,
		DaemonRuntime{},
		localStartupState{},
	)
	queue := &acceptedActionQueue{pending: []string{actionID}}
	transport.actionQueues[processID] = queue
	transport.runActionQueue(context.Background(), processID, queue)

	if !transport.idle() {
		t.Fatal("forgotten action retained transport state")
	}
	select {
	case err := <-transport.fatal:
		t.Fatalf("forgotten action failed the transport: %v", err)
	default:
	}
}
