package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/skillsync"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
)

const daemonSocketReadLimitBytes = daemonprotocol.MaxMessageBytes

type pendingProcess struct {
	runtime            *processRuntime
	accepted           bool
	closing            bool
	terminationReason  string
	terminationApplied bool
}

type pendingAction struct {
	processID           string
	action              ProcessAction
	processState        daemonprotocol.ProcessState
	defaultOutputCursor int64
	enqueued            bool
}

type acceptedActionQueue struct {
	pending []string
}

type daemonReportAck struct {
	status daemonprotocol.AckStatus
	code   string
	err    string
}

type daemonSocketTransport struct {
	client  *Client
	runtime DaemonRuntime
	startup localStartupState

	conn  *websocket.Conn
	send  chan daemonprotocol.Message
	done  chan struct{}
	fatal chan error

	skillsync *skillsync.Manager

	workersMu sync.Mutex
	workers   sync.WaitGroup
	stopping  bool

	mu               sync.Mutex
	pendingProcesses map[string]*pendingProcess
	pendingActions   map[string]pendingAction
	actionQueues     map[string]*acceptedActionQueue
	reportAcks       map[string][]chan daemonReportAck
}

type daemonReportTransport interface {
	SendReport(context.Context, statedb.Report) (daemonReportAck, error)
}

func newDaemonSocketTransport(
	client *Client,
	runtime DaemonRuntime,
	startup localStartupState,
) *daemonSocketTransport {
	return &daemonSocketTransport{
		client:           client,
		runtime:          runtime,
		startup:          startup,
		send:             make(chan daemonprotocol.Message, 128),
		done:             make(chan struct{}),
		fatal:            make(chan error, 1),
		pendingProcesses: make(map[string]*pendingProcess),
		pendingActions:   make(map[string]pendingAction),
		actionQueues:     make(map[string]*acceptedActionQueue),
		reportAcks:       make(map[string][]chan daemonReportAck),
	}
}

func (c *Client) setTransport(transport *daemonSocketTransport) {
	c.transportMu.Lock()
	c.transport = transport
	c.transportMu.Unlock()
}

func (c *Client) clearTransport(transport *daemonSocketTransport) {
	c.transportMu.Lock()
	if c.transport == transport {
		c.transport = nil
	}
	c.transportMu.Unlock()
}

func (c *Client) currentTransport() daemonReportTransport {
	c.transportMu.RLock()
	defer c.transportMu.RUnlock()
	return c.transport
}

func (t *daemonSocketTransport) Run(ctx context.Context) (bool, error) {
	heartbeatAcknowledged := false
	runCtx, cancel := context.WithCancel(ctx)
	defer t.stopAndWait(cancel)

	conn, response, err := websocket.Dial(
		runCtx,
		t.client.daemonSocketURL(t.runtime),
		&websocket.DialOptions{
			HTTPClient: t.client.http,
			HTTPHeader: http.Header{
				"Authorization": {
					"Bearer " + t.client.cfg.MachineToken,
				},
			},
		},
	)
	if response != nil && response.Body != nil {
		defer func() { _ = response.Body.Close() }()
	}
	if err != nil {
		if response != nil {
			switch response.StatusCode {
			case http.StatusGone:
				return false, errDaemonRuntimeUnregistered
			case http.StatusUnauthorized, http.StatusForbidden:
				return false, errDaemonAuthorizationRevoked
			default:
				var body []byte
				var readErr error
				if response.Body != nil {
					body, readErr = readDaemonHTTPResponse(response.Body)
				}
				return false, errors.Join(
					err,
					newHTTPStatusError(response, body),
					readErr,
				)
			}
		}
		return false, err
	}
	t.conn = conn
	conn.SetReadLimit(daemonSocketReadLimitBytes)

	writeDone := make(chan error, 1)
	t.launchWorker(func() { writeDone <- t.writeLoop(runCtx) })
	t.launchWorker(func() {
		t.client.replayReportOutbox(
			runCtx,
			t.startup.ForcedReports,
		)
	})
	t.launchWorker(func() {
		t.client.finalizeReleasedProcessesLoop(runCtx)
	})
	t.resumeStartupActions(runCtx)
	t.reportReadyStorageFailures(runCtx)

	heartbeatDelay, err := nextHeartbeatDelay(t.runtime)
	if err != nil {
		return false, err
	}
	heartbeatAfter := heartbeatDelay
	heartbeatTimer := time.NewTimer(heartbeatDelay)
	defer heartbeatTimer.Stop()
	var idleTick <-chan time.Time
	var idleSince time.Time
	if t.client.sleepEnabled() {
		idleTicker := time.NewTicker(daemonIdleTickInterval)
		defer idleTicker.Stop()
		idleTick = idleTicker.C
	}

	messages := make(chan daemonprotocol.Message)
	readDone := make(chan error, 1)
	t.launchWorker(func() {
		for {
			var message daemonprotocol.Message
			readCtx, cancelRead := context.WithTimeout(
				runCtx,
				70*time.Second,
			)
			err := wsjson.Read(readCtx, conn, &message)
			cancelRead()
			if err != nil {
				readDone <- err
				return
			}
			select {
			case messages <- message:
			case <-runCtx.Done():
				readDone <- runCtx.Err()
				return
			}
		}
	})

	for {
		select {
		case <-runCtx.Done():
			return heartbeatAcknowledged, runCtx.Err()
		case err := <-t.fatal:
			return heartbeatAcknowledged, err
		case <-heartbeatTimer.C:
			if err := t.client.verifyProcessSupervisors(runCtx); err != nil {
				return heartbeatAcknowledged, err
			}
			t.reportReadyStorageFailures(runCtx)
			if !t.enqueue(daemonprotocol.Message{
				Type:             daemonprotocol.MessageHeartbeat,
				DaemonInstanceID: t.client.instanceID,
				ObservedPlatform: mustMarshalRaw(
					daemonObservedPlatform(),
				),
			}) {
				return heartbeatAcknowledged, errors.New(
					"daemon websocket send queue is full",
				)
			}
			heartbeatTimer.Reset(heartbeatAfter)
		case err := <-writeDone:
			return heartbeatAcknowledged, err
		case err := <-readDone:
			return heartbeatAcknowledged, err
		case now := <-idleTick:
			if t.idle() && t.client.daemonIdle(runCtx) {
				if idleSince.IsZero() {
					idleSince = now
				} else if now.Sub(idleSince) >= t.client.cfg.SleepAfter {
					return heartbeatAcknowledged, errDaemonIdleSleep
				}
			} else {
				idleSince = time.Time{}
			}
		case message := <-messages:
			if message.Type != daemonprotocol.MessageHeartbeatAck {
				idleSince = time.Time{}
			}
			if message.Type == daemonprotocol.MessageHeartbeatAck &&
				message.NextHeartbeatAfterMS > 0 {
				heartbeatAcknowledged = true
				heartbeatAfter = time.Duration(
					message.NextHeartbeatAfterMS,
				) * time.Millisecond
			}
			if err := t.handleMessage(runCtx, message); err != nil {
				return heartbeatAcknowledged, err
			}
		}
	}
}

func (t *daemonSocketTransport) idle() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	skillsIdle := t.skillsync == nil || t.skillsync.Idle()
	return len(t.pendingProcesses) == 0 &&
		len(t.pendingActions) == 0 &&
		len(t.actionQueues) == 0 &&
		len(t.reportAcks) == 0 &&
		skillsIdle
}

func (t *daemonSocketTransport) resumeStartupActions(ctx context.Context) {
	sort.Slice(t.startup.Actions, func(i, j int) bool {
		if t.startup.Actions[i].processID !=
			t.startup.Actions[j].processID {
			return t.startup.Actions[i].processID <
				t.startup.Actions[j].processID
		}
		return t.startup.Actions[i].action.Seq <
			t.startup.Actions[j].action.Seq
	})
	for _, action := range t.startup.Actions {
		if processRuntimeStorageExhaustionReady(t.startup.Runners[action.processID]) {
			continue
		}
		accepted := pendingAction{
			processID: action.processID,
			action:    action.action,
			enqueued:  true,
		}
		t.mu.Lock()
		t.pendingActions[action.action.ID] = accepted
		t.mu.Unlock()
		t.enqueueAcceptedAction(ctx, accepted)
	}
}

func (t *daemonSocketTransport) launchWorker(work func()) bool {
	t.workersMu.Lock()
	if t.stopping {
		t.workersMu.Unlock()
		return false
	}
	t.workers.Add(1)
	t.workersMu.Unlock()
	go func() {
		defer t.workers.Done()
		work()
	}()
	return true
}

// Registration must not snapshot local process state until the previous socket's workers
// have finished any irreversible local transition.
func (t *daemonSocketTransport) stopAndWait(cancel context.CancelFunc) {
	t.workersMu.Lock()
	t.stopping = true
	t.workersMu.Unlock()

	cancel()
	if t.conn != nil {
		_ = t.conn.Close(websocket.StatusNormalClosure, "closing")
	}
	t.failAllReports(errors.New("daemon websocket closed"))
	close(t.done)
	t.workers.Wait()
}

func (t *daemonSocketTransport) reportReadyStorageFailures(ctx context.Context) {
	for _, runtime := range t.client.storageFailureRuntimes() {
		t.launchWorker(func() {
			_, err := t.client.handleAcceptedStorageFailure(
				ctx,
				t,
				runtime,
				errStorageExhaustionTerminalReady,
			)
			if err != nil &&
				ctx.Err() == nil {
				t.fail(fmt.Errorf(
					"report storage exhaustion for process %s: %w",
					runtime.processID,
					err,
				))
			}
		})
	}
}

// An unlocked supervisor is ambiguous until PostgreSQL authorizes cleanup;
// afterward the same row only tracks pending local cleanup.
func (c *Client) verifyProcessSupervisors(ctx context.Context) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	processes, err := stateStore.Processes(ctx)
	if err != nil {
		return err
	}
	machine, err := c.machineStore()
	if err != nil {
		return err
	}
	for _, process := range processes {
		if process.LocalClosed {
			c.removeProcessInstance(
				process.ProcessID,
				process.SupervisorInstanceID,
			)
			continue
		}
		if runtime, found := c.localProcess(process.ProcessID); found &&
			runtime.supervisorInstanceID == process.SupervisorInstanceID {
			if runtime.cleanupOnly {
				continue
			}
			if runtime.runner != nil {
				probeCtx, cancel := context.WithTimeout(ctx, 250*time.Millisecond)
				// Status records a storage-ready sentinel on the IPC runner; other errors are ignored.
				_ = runtime.runner.Status(probeCtx)
				cancel()
			}
		}
		switch process.Phase {
		case statedb.ProcessPrepared,
			statedb.ProcessAccepted,
			statedb.ProcessTerminal:
		case statedb.ProcessPreparing:
			// Preparation owns the gap between reservation and lock acquisition.
			continue
		default:
			return fmt.Errorf(
				"process %s has invalid local phase %q",
				process.ProcessID,
				process.Phase,
			)
		}

		lockPath, err := machine.LifetimeLockPath(process.ProcessID)
		if err != nil {
			return err
		}
		lock, lockErr := localstore.TryAcquireExistingLock(lockPath)
		lockMissing := false
		switch {
		case errors.Is(lockErr, localstore.ErrLockHeld):
			continue
		case lockErr == nil:
			if err := lock.Release(); err != nil {
				return err
			}
		case errors.Is(lockErr, os.ErrNotExist):
			lockMissing = true
		default:
			return fmt.Errorf(
				"inspect process %s supervisor instance %s lifetime: %w",
				process.ProcessID,
				process.SupervisorInstanceID,
				lockErr,
			)
		}
		current, found, err := stateStore.Process(ctx, process.ProcessID)
		if err != nil {
			return err
		}
		if !found ||
			current.SupervisorInstanceID != process.SupervisorInstanceID ||
			current.LocalClosed {
			c.removeProcessInstance(
				process.ProcessID,
				process.SupervisorInstanceID,
			)
			continue
		}
		if current.ServerReleased && !lockMissing {
			c.removeProcessInstance(
				process.ProcessID,
				process.SupervisorInstanceID,
			)
			continue
		}
		return fmt.Errorf(
			"process %s supervisor instance %s no longer holds its lifetime lock",
			process.ProcessID,
			process.SupervisorInstanceID,
		)
	}
	return nil
}

func (t *daemonSocketTransport) writeLoop(ctx context.Context) error {
	pingTicker := time.NewTicker(20 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-pingTicker.C:
			pingCtx, cancel := context.WithTimeout(
				ctx,
				10*time.Second,
			)
			err := t.conn.Ping(pingCtx)
			cancel()
			if err != nil {
				return err
			}
		case message := <-t.send:
			writeCtx, cancel := context.WithTimeout(
				ctx,
				10*time.Second,
			)
			err := wsjson.Write(writeCtx, t.conn, &message)
			cancel()
			if err != nil {
				return err
			}
		}
	}
}

func (t *daemonSocketTransport) handleMessage(
	ctx context.Context,
	message daemonprotocol.Message,
) error {
	switch message.Type {
	case daemonprotocol.MessageProcessOffer:
		if message.ProcessOffer != nil {
			t.offerProcess(ctx, *message.ProcessOffer)
		}
	case daemonprotocol.MessageProcessAcceptAck:
		t.startAcceptedProcess(ctx, message.ProcessID)
	case daemonprotocol.MessageProcessTerminate:
		t.terminateProcess(ctx, message.ProcessID)
	case daemonprotocol.MessageActionOffer:
		if message.ActionOffer != nil {
			t.offerAction(*message.ActionOffer)
		}
	case daemonprotocol.MessageActionAcceptAck:
		if message.ActionGrant != nil {
			t.applyAcceptedAction(ctx, *message.ActionGrant)
		}
	case daemonprotocol.MessageReportAck:
		t.ackReport(message)
	case daemonprotocol.MessageRuntimeEnded:
		switch message.ErrorCode {
		case daemonprotocol.ErrorCodeMachineDecommissioned:
			return errMachineDecommissioned
		case daemonprotocol.ErrorCodeAuthorizationRevoked:
			return errDaemonAuthorizationRevoked
		default:
			return errDaemonRuntimeUnregistered
		}
	case daemonprotocol.MessageError:
		t.handleServerError(ctx, message)
	case daemonprotocol.MessageSkillOffer:
		if message.SkillOffer != nil {
			t.handleSkillOffer(ctx, *message.SkillOffer)
		}
	case daemonprotocol.MessageHeartbeatAck:
	case daemonprotocol.MessageHeartbeat,
		daemonprotocol.MessageProcessAccept,
		daemonprotocol.MessageActionAccept,
		daemonprotocol.MessageReport,
		daemonprotocol.MessageSkillReport:
		return fmt.Errorf(
			"unexpected server-to-daemon message type %q",
			message.Type,
		)
	default:
		return nil
	}
	return nil
}

func (t *daemonSocketTransport) handleServerError(
	ctx context.Context,
	message daemonprotocol.Message,
) {
	switch message.ErrorCode {
	case daemonprotocol.ErrorCodeProcessOfferUnavailable:
		t.closeRejectedProcessOffer(ctx, message.ProcessID)
		return
	case daemonprotocol.ErrorCodeActionOfferUnavailable:
		t.forgetRejectedActionOffer(
			message.ProcessID,
			message.ProcessActionID,
		)
		return
	}
	t.client.log.Warn(
		"daemon websocket server error",
		"error_code",
		message.ErrorCode,
		"error",
		message.Error,
	)
	if message.ProcessID != "" {
		t.fail(fmt.Errorf(
			"daemon acceptance outcome for process %s is ambiguous: %s",
			message.ProcessID,
			message.Error,
		))
	}
}

func (t *daemonSocketTransport) closeRejectedProcessOffer(
	ctx context.Context,
	processID string,
) {
	t.mu.Lock()
	pending := t.pendingProcesses[processID]
	switch {
	case pending == nil:
		t.mu.Unlock()
		return
	case pending.runtime == nil:
		delete(t.pendingProcesses, processID)
		t.mu.Unlock()
		t.fail(fmt.Errorf(
			"server rejected process %s before its local preparation completed",
			processID,
		))
		return
	case pending.closing:
		t.mu.Unlock()
		return
	}
	pending.closing = true
	t.mu.Unlock()
	t.launchWorker(func() {
		cleanupPending, err := t.client.closeRejectedPreparation(
			ctx,
			processID,
			pending.runtime.supervisorInstanceID,
			pending.runtime,
			nil,
		)
		if cleanupPending {
			t.client.addProcess(&processRuntime{
				processID:            processID,
				supervisorInstanceID: pending.runtime.supervisorInstanceID,
				cleanupOnly:          true,
			})
			t.client.log.Warn(
				"rejected process preparation cleanup remains pending",
				"process_id",
				processID,
				"error",
				err,
			)
		} else if err != nil {
			t.fail(fmt.Errorf(
				"close rejected process preparation %s: %w",
				processID,
				err,
			))
			return
		}
		t.mu.Lock()
		if t.pendingProcesses[processID] == pending {
			delete(t.pendingProcesses, processID)
		}
		t.mu.Unlock()
	})
}

func (t *daemonSocketTransport) forgetRejectedActionOffer(
	processID, actionID string,
) {
	t.mu.Lock()
	defer t.mu.Unlock()
	action, ok := t.pendingActions[actionID]
	if !ok {
		return
	}
	if action.processID != processID {
		t.fail(fmt.Errorf(
			"server rejected process action %s for process %s, want %s",
			actionID,
			processID,
			action.processID,
		))
		return
	}
	delete(t.pendingActions, actionID)
}

func (t *daemonSocketTransport) offerProcess(
	ctx context.Context,
	offer daemonprotocol.ProcessOffer,
) {
	assignment := ProcessAssignment{
		ID: offer.ProcessID,
		Process: Process{
			Command:       offer.Command,
			ShellSelector: offer.ShellSelector,
			Cwd:           offer.Cwd,
			IOMode:        offer.IOMode,
		},
		Env:              offer.Env,
		PreparationError: offer.PreparationError,
		WaitMs:           offer.WaitMs,
		TimeoutSeconds:   offer.TimeoutSeconds,
	}

	t.mu.Lock()
	pending := t.pendingProcesses[offer.ProcessID]
	if pending != nil {
		t.mu.Unlock()
		return
	}
	if _, exists := t.client.localProcess(offer.ProcessID); exists {
		t.mu.Unlock()
		return
	}
	pending = &pendingProcess{}
	t.pendingProcesses[offer.ProcessID] = pending
	t.mu.Unlock()

	t.launchWorker(func() {
		runtime, err := t.client.runnerLauncher.Prepare(
			ctx,
			t.client,
			assignment,
		)
		if err != nil {
			storageFailure := isStorageExhaustion(err) &&
				!errors.Is(err, errUnresolvedProcessPreparation)
			if runtime != nil && runtime.cleanupOnly {
				t.client.addProcess(runtime)
			} else if errors.Is(err, errUnresolvedProcessPreparation) {
				t.fail(fmt.Errorf(
					"process preparation %s retained unresolved local state: %w",
					assignment.ID,
					err,
				))
			}
			t.client.log.Warn(
				"prepare process supervisor failed",
				"process_id",
				assignment.ID,
				"error",
				err,
			)
			if storageFailure {
				if reportErr := sendStorageExhaustionReport(
					ctx,
					t,
					assignment.ID,
				); reportErr != nil && ctx.Err() == nil {
					t.fail(fmt.Errorf(
						"report storage exhaustion for process %s: %w",
						assignment.ID,
						reportErr,
					))
				}
			}
			t.mu.Lock()
			if t.pendingProcesses[assignment.ID] == pending {
				delete(t.pendingProcesses, assignment.ID)
			}
			t.mu.Unlock()
			return
		}
		t.mu.Lock()
		current := t.pendingProcesses[assignment.ID]
		if current != pending {
			t.mu.Unlock()
			cleanupPending, err := t.client.closeRejectedPreparation(
				ctx,
				assignment.ID,
				runtime.supervisorInstanceID,
				runtime,
				nil,
			)
			if cleanupPending {
				t.client.addProcess(&processRuntime{
					processID:            assignment.ID,
					supervisorInstanceID: runtime.supervisorInstanceID,
					cleanupOnly:          true,
				})
				t.client.log.Warn(
					"superseded process preparation cleanup remains pending",
					"process_id",
					assignment.ID,
					"error",
					err,
				)
				return
			}
			if err != nil {
				t.fail(fmt.Errorf(
					"close superseded process preparation %s: %w",
					assignment.ID,
					err,
				))
			}
			return
		}
		pending.runtime = runtime
		t.mu.Unlock()
		t.sendProcessAccept(assignment.ID)
	})
}

func (t *daemonSocketTransport) sendProcessAccept(processID string) {
	if !t.enqueue(daemonprotocol.Message{
		Type:      daemonprotocol.MessageProcessAccept,
		ProcessID: processID,
	}) {
		t.fail(errors.New("daemon websocket send queue is full"))
	}
}

func (t *daemonSocketTransport) startAcceptedProcess(
	ctx context.Context,
	processID string,
) {
	t.mu.Lock()
	pending := t.pendingProcesses[processID]
	if pending == nil || pending.runtime == nil || pending.accepted {
		t.mu.Unlock()
		return
	}
	pending.accepted = true
	t.mu.Unlock()

	t.launchWorker(func() {
		stateStore, err := t.client.stateStore(ctx)
		if err == nil {
			err = retryWhileStateDBBusy(ctx, func() error {
				return stateStore.MarkAccepted(
					ctx,
					processID,
					pending.runtime.supervisorInstanceID,
				)
			})
		}
		if err == nil {
			t.mu.Lock()
			terminationReason := pending.terminationReason
			if terminationReason != "" {
				pending.terminationApplied = true
			}
			t.mu.Unlock()
			if terminationReason != "" {
				terminateCtx, cancel := context.WithTimeout(
					context.WithoutCancel(ctx),
					10*time.Second,
				)
				err = pending.runtime.runner.Terminate(
					terminateCtx,
					terminationReason,
				)
				cancel()
			} else {
				err = pending.runtime.runner.StartOnce(ctx)
			}
		}
		storageFailure, err := t.client.handleAcceptedStorageFailure(
			ctx,
			t,
			pending.runtime,
			err,
		)
		if storageFailure {
			if err == nil {
				t.mu.Lock()
				if t.pendingProcesses[processID] == pending {
					delete(t.pendingProcesses, processID)
				}
				t.mu.Unlock()
			}
			if err != nil {
				t.fail(fmt.Errorf("start accepted process %s: %w", processID, err))
			}
			return
		}
		if err != nil {
			t.fail(fmt.Errorf(
				"start accepted process %s: %w",
				processID,
				err,
			))
			return
		}
		t.mu.Lock()
		current := t.pendingProcesses[processID]
		if current != pending {
			t.mu.Unlock()
			t.fail(fmt.Errorf(
				"accepted process %s lost its pending runtime",
				processID,
			))
			return
		}
		t.client.addProcess(pending.runtime)
		delete(t.pendingProcesses, processID)
		terminationReason := pending.terminationReason
		terminationApplied := pending.terminationApplied
		t.mu.Unlock()
		if terminationReason != "" && !terminationApplied {
			terminateCtx, cancel := context.WithTimeout(
				context.WithoutCancel(ctx),
				10*time.Second,
			)
			err := pending.runtime.runner.Terminate(
				terminateCtx,
				terminationReason,
			)
			cancel()
			if err != nil {
				t.fail(fmt.Errorf(
					"terminate newly accepted process %s: %w",
					processID,
					err,
				))
			}
		}
	})
}

func (t *daemonSocketTransport) offerAction(
	offer daemonprotocol.ActionOffer,
) {
	if offer.ActionKind != daemonprotocol.ProcessActionRead {
		runtime, exists := t.client.localProcess(offer.ProcessID)
		if !exists || runtime.cleanupOnly || runtime.runner == nil {
			return
		}
	}
	if !offer.ActionKind.Valid() {
		return
	}
	pending := pendingAction{
		processID: offer.ProcessID,
		action: ProcessAction{
			ID:         offer.ProcessActionID,
			ActionKind: offer.ActionKind,
			Seq:        offer.Seq,
			Payload:    offer.Payload,
		},
	}
	t.mu.Lock()
	existing, known := t.pendingActions[offer.ProcessActionID]
	if known && !samePendingAction(existing, pending) {
		t.mu.Unlock()
		t.fail(fmt.Errorf(
			"process action %s identity changed",
			offer.ProcessActionID,
		))
		return
	}
	if known {
		t.mu.Unlock()
		return
	}
	t.pendingActions[offer.ProcessActionID] = pending
	t.mu.Unlock()
	if !t.enqueue(daemonprotocol.Message{
		Type:            daemonprotocol.MessageActionAccept,
		ProcessID:       offer.ProcessID,
		ProcessActionID: offer.ProcessActionID,
	}) {
		t.fail(errors.New("daemon websocket send queue is full"))
	}
}

func (t *daemonSocketTransport) applyAcceptedAction(
	ctx context.Context,
	grant daemonprotocol.ActionGrant,
) {
	t.mu.Lock()
	accepted, ok := t.pendingActions[grant.ProcessActionID]
	if !ok || accepted.processID != grant.ProcessID {
		t.mu.Unlock()
		return
	}
	if accepted.enqueued {
		// Reconciliation may have enqueued it before this delayed acknowledgement.
		t.mu.Unlock()
		return
	}
	accepted.processState = grant.ProcessState
	accepted.defaultOutputCursor = grant.DefaultOutputCursor
	accepted.enqueued = true
	t.pendingActions[grant.ProcessActionID] = accepted
	t.mu.Unlock()
	t.enqueueAcceptedAction(ctx, accepted)
}

func samePendingAction(left, right pendingAction) bool {
	return left.processID == right.processID &&
		left.action.ID == right.action.ID &&
		left.action.ActionKind == right.action.ActionKind &&
		left.action.Seq == right.action.Seq &&
		string(left.action.Payload) == string(right.action.Payload)
}

func (t *daemonSocketTransport) enqueueAcceptedAction(
	ctx context.Context,
	action pendingAction,
) {
	t.mu.Lock()
	queue := t.actionQueues[action.processID]
	start := queue == nil
	if start {
		queue = &acceptedActionQueue{}
		t.actionQueues[action.processID] = queue
	}
	queue.pending = append(queue.pending, action.action.ID)
	t.mu.Unlock()
	if start {
		t.launchWorker(func() {
			t.runActionQueue(ctx, action.processID, queue)
		})
	}
}

func (t *daemonSocketTransport) runActionQueue(
	ctx context.Context,
	processID string,
	queue *acceptedActionQueue,
) {
	for {
		actionID, ok := t.dequeueAcceptedAction(processID, queue)
		if !ok {
			return
		}
		if err := t.runAcceptedAction(
			ctx,
			processID,
			actionID,
		); err != nil {
			t.mu.Lock()
			_, pending := t.pendingActions[actionID]
			if pending && ctx.Err() == nil {
				t.fail(err)
			}
			t.mu.Unlock()
			if pending {
				return
			}
		}
	}
}

func (t *daemonSocketTransport) dequeueAcceptedAction(
	processID string,
	queue *acceptedActionQueue,
) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(queue.pending) == 0 {
		if t.actionQueues[processID] == queue {
			delete(t.actionQueues, processID)
		}
		return "", false
	}
	actionID := queue.pending[0]
	queue.pending[0] = ""
	queue.pending = queue.pending[1:]
	if len(queue.pending) == 0 {
		queue.pending = nil
	}
	return actionID, true
}

func (t *daemonSocketTransport) runAcceptedAction(
	ctx context.Context,
	processID, actionID string,
) error {
	t.mu.Lock()
	accepted, known := t.pendingActions[actionID]
	t.mu.Unlock()
	if !known {
		return fmt.Errorf("queued action %s lost its accepted package", actionID)
	}
	if accepted.processID != processID {
		return fmt.Errorf(
			"queued action %s names process %s, want %s",
			actionID,
			accepted.processID,
			processID,
		)
	}
	if accepted.action.ActionKind == daemonprotocol.ProcessActionRead {
		if err := t.runAcceptedRead(ctx, accepted); err != nil {
			return err
		}
	} else {
		runtime, ok := t.client.localProcess(processID)
		if !ok || runtime.cleanupOnly || runtime.runner == nil {
			return fmt.Errorf(
				"accepted action %s lost its live supervisor",
				actionID,
			)
		}
		for {
			err := runtime.runner.ApplyOnce(ctx, accepted.action)
			if errors.Is(err, statedb.ErrActionBlocked) {
				if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
					return err
				}
				continue
			}
			storageFailure, storageErr := t.client.handleAcceptedStorageFailure(
				ctx,
				t,
				runtime,
				err,
			)
			if storageFailure {
				return storageErr
			}
			if storageErr != nil {
				return fmt.Errorf(
					"apply accepted process action %s: %w",
					accepted.action.ID,
					storageErr,
				)
			}
			break
		}
		if err := t.waitActionReleased(ctx, accepted.action.ID); err != nil {
			return fmt.Errorf(
				"wait for accepted process action %s release: %w",
				accepted.action.ID,
				err,
			)
		}
	}
	t.mu.Lock()
	delete(t.pendingActions, accepted.action.ID)
	t.mu.Unlock()
	return nil
}

func (t *daemonSocketTransport) forgetResolvedProcessActions(processID string) {
	t.mu.Lock()
	for actionID, action := range t.pendingActions {
		if action.processID == processID {
			delete(t.pendingActions, actionID)
		}
	}
	if queue := t.actionQueues[processID]; queue != nil {
		queue.pending = nil
	}
	t.mu.Unlock()
}

func (t *daemonSocketTransport) runAcceptedRead(
	ctx context.Context,
	accepted pendingAction,
) error {
	payload, err := processaction.DecodeReadPayload(accepted.action.Payload)
	if err != nil {
		return t.reportDirectReadFailure(
			ctx,
			accepted,
			daemonprotocol.ProcessActionReasonInvalidPayload,
			err,
		)
	}
	machine, err := t.client.machineStore()
	if err != nil {
		return err
	}
	processDir, err := machine.ProcessDir(accepted.processID)
	if err != nil {
		return err
	}
	root, err := os.OpenRoot(processDir)
	if err != nil {
		return t.reportDirectReadFailure(
			ctx,
			accepted,
			daemonprotocol.ProcessActionReasonOutputUnavailable,
			err,
		)
	}
	defer func() { _ = root.Close() }()

	cursor := accepted.defaultOutputCursor
	if payload.Cursor != nil {
		cursor = *payload.Cursor
	}
	deadline := time.Now().Add(time.Duration(payload.WaitMS) * time.Millisecond)
	for {
		output, start, next, truncated, readErr := readProcessOutputSnapshot(
			root,
			cursor,
			payload.MaxBytes,
		)
		if readErr != nil {
			return t.reportDirectReadFailure(
				ctx,
				accepted,
				daemonprotocol.ProcessActionReasonOutputUnavailable,
				readErr,
			)
		}
		terminal, terminalErr := t.readProcessTerminal(
			ctx,
			accepted.processID,
			accepted.processState,
		)
		if terminalErr != nil {
			return terminalErr
		}
		if output != "" || terminal || payload.WaitMS == 0 ||
			!time.Now().Before(deadline) {
			result, err := marshalJSON(processaction.ReadObservation{
				ProcessID:  accepted.processID,
				Output:     output,
				Cursor:     start,
				NextCursor: next,
				Truncated:  truncated,
			})
			if err != nil {
				return err
			}
			return t.reportDirectRead(
				ctx,
				accepted,
				daemonprotocol.EventProcessActionApplied,
				"",
				"",
				result,
			)
		}
		if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

func (t *daemonSocketTransport) readProcessTerminal(
	ctx context.Context,
	processID string,
	grantedState daemonprotocol.ProcessState,
) (bool, error) {
	switch grantedState {
	case daemonprotocol.ProcessStateExited,
		daemonprotocol.ProcessStateFailed,
		daemonprotocol.ProcessStateKilled,
		daemonprotocol.ProcessStateUnknown:
		return true, nil
	case "",
		daemonprotocol.ProcessStateQueued,
		daemonprotocol.ProcessStateStarting,
		daemonprotocol.ProcessStateRunning:
	default:
		return false, fmt.Errorf(
			"unsupported granted process state %q",
			grantedState,
		)
	}
	stateStore, err := t.client.stateStore(ctx)
	if err != nil {
		return false, err
	}
	process, found, err := stateStore.Process(ctx, processID)
	if err != nil {
		return false, err
	}
	return !found || process.ServerReleased, nil
}

func (t *daemonSocketTransport) reportDirectReadFailure(
	ctx context.Context,
	accepted pendingAction,
	code string,
	cause error,
) error {
	return t.reportDirectRead(
		ctx,
		accepted,
		daemonprotocol.EventProcessActionFailed,
		code,
		cause.Error(),
		nil,
	)
}

func (t *daemonSocketTransport) reportDirectRead(
	ctx context.Context,
	accepted pendingAction,
	eventType daemonprotocol.ReportedEventType,
	reasonCode, reasonMessage string,
	result json.RawMessage,
) error {
	body, err := json.Marshal(daemonReportedEvent{
		Type:               eventType,
		ProcessID:          accepted.processID,
		ProcessActionID:    accepted.action.ID,
		StateReasonCode:    reasonCode,
		StateReasonMessage: reasonMessage,
		Result:             result,
	})
	if err != nil {
		return err
	}
	ack, err := t.SendReport(ctx, statedb.Report{
		ID:        uuid.NewString(),
		ProcessID: accepted.processID,
		ActionID:  accepted.action.ID,
		Kind:      statedb.ReportActionTerminal,
		Body:      body,
	})
	if err != nil {
		return err
	}
	switch ack.status {
	case daemonprotocol.AckStatusCommitted,
		daemonprotocol.AckStatusCleanupOnly:
	case daemonprotocol.AckStatusPermanentReject:
		return fmt.Errorf(
			"process read report was permanently rejected (%s): %s",
			ack.code,
			ack.err,
		)
	case daemonprotocol.AckStatusTransientError:
		return fmt.Errorf(
			"process read report was temporarily rejected (%s): %s",
			ack.code,
			ack.err,
		)
	default:
		return fmt.Errorf(
			"unsupported process read report acknowledgement %q",
			ack.status,
		)
	}
	stateStore, err := t.client.stateStore(ctx)
	if err != nil {
		return err
	}
	process, found, err := stateStore.Process(ctx, accepted.processID)
	if err != nil || !found {
		return err
	}
	return retryWhileStateDBBusy(ctx, func() error {
		return stateStore.ResolveAbsentAction(
			ctx,
			accepted.processID,
			process.SupervisorInstanceID,
			accepted.action.ID,
			accepted.action.Seq,
		)
	})
}

func (t *daemonSocketTransport) waitActionReleased(
	ctx context.Context,
	actionID string,
) error {
	stateStore, err := t.client.stateStore(ctx)
	if err != nil {
		return err
	}
	for {
		_, found, err := stateStore.Action(ctx, actionID)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

func (t *daemonSocketTransport) terminateProcess(
	ctx context.Context,
	processID string,
) {
	t.mu.Lock()
	if pending := t.pendingProcesses[processID]; pending != nil {
		pending.terminationReason = "server_requested"
		t.mu.Unlock()
		return
	}
	runtime, ok := t.client.localProcess(processID)
	t.mu.Unlock()
	if !ok || runtime.cleanupOnly || runtime.runner == nil {
		return
	}
	t.launchWorker(func() {
		terminateCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			10*time.Second,
		)
		err := runtime.runner.Terminate(
			terminateCtx,
			"server_requested",
		)
		cancel()
		if err != nil {
			t.fail(fmt.Errorf(
				"terminate process %s requested by server: %w",
				processID,
				err,
			))
		}
	})
}

func (t *daemonSocketTransport) SendReport(
	ctx context.Context,
	report statedb.Report,
) (daemonReportAck, error) {
	if report.ID == "" {
		return daemonReportAck{}, errors.New("daemon report ID is required")
	}
	var event daemonReportedEvent
	if err := json.Unmarshal(report.Body, &event); err != nil {
		return daemonReportAck{}, err
	}
	ack := make(chan daemonReportAck, 1)
	t.mu.Lock()
	first := len(t.reportAcks[report.ID]) == 0
	t.reportAcks[report.ID] = append(t.reportAcks[report.ID], ack)
	t.mu.Unlock()
	if first && !t.enqueue(daemonprotocol.Message{
		Type:     daemonprotocol.MessageReport,
		ReportID: report.ID,
		Event:    &event,
	}) {
		t.failReport(report.ID, daemonReportAck{
			status: daemonprotocol.AckStatusTransientError,
			err:    "daemon websocket send queue is full",
		})
	}
	select {
	case <-ctx.Done():
		t.removeReportWaiter(report.ID, ack)
		return daemonReportAck{}, ctx.Err()
	case <-t.done:
		return daemonReportAck{}, errors.New(
			"daemon websocket closed before report acknowledgement",
		)
	case result := <-ack:
		return result, nil
	}
}

func (t *daemonSocketTransport) ackReport(
	message daemonprotocol.Message,
) {
	t.failReport(message.ReportID, daemonReportAck{
		status: message.AckStatus,
		code:   message.ErrorCode,
		err:    message.Error,
	})
}

func (t *daemonSocketTransport) removeReportWaiter(
	reportID string,
	waiter chan daemonReportAck,
) {
	t.mu.Lock()
	defer t.mu.Unlock()
	waiters := t.reportAcks[reportID]
	for index, candidate := range waiters {
		if candidate != waiter {
			continue
		}
		waiters = append(waiters[:index], waiters[index+1:]...)
		if len(waiters) == 0 {
			delete(t.reportAcks, reportID)
		} else {
			t.reportAcks[reportID] = waiters
		}
		return
	}
}

func (t *daemonSocketTransport) failReport(
	reportID string,
	ack daemonReportAck,
) {
	t.mu.Lock()
	waiters := t.reportAcks[reportID]
	delete(t.reportAcks, reportID)
	t.mu.Unlock()
	for _, waiter := range waiters {
		waiter <- ack
	}
}

func (t *daemonSocketTransport) failAllReports(err error) {
	t.mu.Lock()
	waiters := t.reportAcks
	t.reportAcks = make(map[string][]chan daemonReportAck)
	t.mu.Unlock()
	for _, reportWaiters := range waiters {
		for _, waiter := range reportWaiters {
			waiter <- daemonReportAck{
				status: daemonprotocol.AckStatusTransientError,
				err:    err.Error(),
			}
		}
	}
}

func (t *daemonSocketTransport) fail(err error) {
	select {
	case t.fatal <- err:
	default:
	}
}

func (t *daemonSocketTransport) enqueue(
	message daemonprotocol.Message,
) bool {
	select {
	case t.send <- message:
		return true
	default:
		return false
	}
}

func (t *daemonSocketTransport) handleSkillOffer(
	ctx context.Context,
	offer daemonprotocol.SkillOffer,
) {
	manager, err := t.skillSyncManager()
	if err != nil {
		t.client.log.Warn(
			"skill offer cannot be handled",
			"skill_id",
			offer.SkillID,
			"error",
			err,
		)
		_ = t.enqueue(daemonprotocol.Message{
			Type: daemonprotocol.MessageSkillReport,
			SkillReport: &daemonprotocol.SkillReport{
				RequestID: offer.RequestID,
				SkillID:   offer.SkillID,
				State:     daemonprotocol.SkillStateFailed,
				ErrorCode: "skillsync_unavailable",
				Error:     err.Error(),
			},
		})
		return
	}
	t.launchWorker(func() {
		manager.HandleOffer(ctx, offer)
	})
}

func (t *daemonSocketTransport) skillSyncManager() (
	*skillsync.Manager,
	error,
) {
	if t.skillsync != nil {
		return t.skillsync, nil
	}
	if t.client.cfg.OmnaraHome == "" {
		return nil, errors.New("OMNARA_HOME is not configured")
	}
	store, err := localstore.Machine(
		t.client.cfg.OmnaraHome,
		t.client.bootstrap.InstallationID,
		t.client.bootstrap.MachineID,
	)
	if err != nil {
		return nil, fmt.Errorf("skillsync localstore: %w", err)
	}
	t.skillsync = skillsync.NewManager(
		store,
		transportSkillSender{transport: t},
		t.client.http.Transport,
		t.client.cfg.APIURL,
		t.client.cfg.MachineToken,
		t.client.log,
	)
	return t.skillsync, nil
}

type transportSkillSender struct {
	transport *daemonSocketTransport
}

func (sender transportSkillSender) SendSkillReport(
	_ context.Context,
	report daemonprotocol.SkillReport,
) error {
	if !sender.transport.enqueue(daemonprotocol.Message{
		Type:        daemonprotocol.MessageSkillReport,
		SkillReport: &report,
	}) {
		return errors.New("transport send queue is full")
	}
	return nil
}

func (c *Client) daemonSocketURL(runtime DaemonRuntime) string {
	raw := c.cfg.APIURL + daemonAPIPath +
		"/runtimes/" + runtime.ID + "/socket"
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	}
	return parsed.String()
}

func nextHeartbeatDelay(runtime DaemonRuntime) (time.Duration, error) {
	if runtime.NextHeartbeatAfterMS <= 0 {
		return 0, errDaemonRuntimeMissingHeartbeatDirective
	}
	return time.Duration(runtime.NextHeartbeatAfterMS) * time.Millisecond, nil
}

func mustMarshalRaw(value any) json.RawMessage {
	body, err := json.Marshal(value)
	if err != nil {
		return nil
	}
	return body
}
