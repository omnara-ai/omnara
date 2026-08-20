package machinedaemon

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func (c *Client) replayReportOutbox(
	ctx context.Context,
	forced map[string]struct{},
) {
	if forced == nil {
		forced = make(map[string]struct{})
	}
	for {
		if err := ctx.Err(); err != nil {
			return
		}
		reports, err := c.outboxReports(ctx, forced)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.log.Warn("load daemon report outbox failed", "error", err)
			if sleepContext(ctx, c.cfg.RetryInterval) != nil {
				return
			}
			continue
		}
		if len(reports) == 0 {
			if sleepContext(ctx, 100*time.Millisecond) != nil {
				return
			}
			continue
		}

		progress := false
		for _, report := range reports {
			ack, err := c.sendFrozenReport(ctx, report)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				c.log.Warn(
					"send daemon report failed",
					"report_id",
					report.ID,
					"error",
					err,
				)
				break
			}
			switch ack.status {
			case daemonprotocol.AckStatusCommitted,
				daemonprotocol.AckStatusCleanupOnly:
				err = c.acknowledgeFrozenReport(ctx, report.ID)
			case daemonprotocol.AckStatusPermanentReject:
				code := ack.code
				if code == "" {
					code = daemonprotocol.ErrorCodeValidationFailed
				}
				stateStore, openErr := c.stateStore(ctx)
				if openErr != nil {
					err = openErr
				} else {
					err = retryWhileStateDBBusy(ctx, func() error {
						return stateStore.RejectReport(
							ctx,
							report.ID,
							code,
							ack.err,
						)
					})
				}
			case daemonprotocol.AckStatusTransientError:
				err = fmt.Errorf(
					"transient report rejection %s: %s",
					ack.code,
					ack.err,
				)
			default:
				err = fmt.Errorf(
					"unsupported daemon report acknowledgement %q",
					ack.status,
				)
			}
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				c.log.Warn(
					"settle daemon report failed",
					"report_id",
					report.ID,
					"ack_status",
					ack.status,
					"error",
					err,
				)
				break
			}
			delete(forced, report.ID)
			progress = true
			if ack.status == daemonprotocol.AckStatusPermanentReject {
				// Recompute this process's report frontier before sending more.
				break
			}
		}
		if !progress &&
			sleepContext(ctx, c.cfg.RetryInterval) != nil {
			return
		}
	}
}

func (c *Client) outboxReports(
	ctx context.Context,
	forced map[string]struct{},
) ([]statedb.Report, error) {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return nil, err
	}
	candidates, err := stateStore.DeliveryCandidates(ctx)
	if err != nil {
		return nil, err
	}
	reports := make([]statedb.Report, 0, min(len(candidates), 128))
	blockedProcesses := make(map[string]struct{})
	blockedActions := make(map[string]struct{})
	for _, report := range candidates {
		c.processMu.RLock()
		runtime := c.processes[report.ProcessID]
		cleanupOnly := runtime != nil && runtime.cleanupOnly
		c.processMu.RUnlock()
		if cleanupOnly {
			continue
		}
		if _, blocked := blockedProcesses[report.ProcessID]; blocked {
			continue
		}
		if _, blocked := blockedActions[report.ProcessID]; blocked &&
			report.Kind != statedb.ReportProcessTerminal {
			continue
		}
		if report.State != statedb.ReportPending {
			if _, wanted := forced[report.ID]; !wanted {
				if report.Kind == statedb.ReportActionTerminal &&
					report.State == statedb.ReportRejected {
					blockedActions[report.ProcessID] = struct{}{}
				} else {
					blockedProcesses[report.ProcessID] = struct{}{}
				}
				continue
			}
		}
		reports = append(reports, report)
		if len(reports) == 128 {
			break
		}
	}
	return reports, nil
}

func (c *Client) sendFrozenReport(
	ctx context.Context,
	report statedb.Report,
) (daemonReportAck, error) {
	transport := c.currentTransport()
	if transport == nil {
		return daemonReportAck{}, errDaemonTransportUnavailable
	}
	return transport.SendReport(ctx, report)
}

func (c *Client) acknowledgeFrozenReport(
	ctx context.Context,
	reportID string,
) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	return retryWhileStateDBBusy(ctx, func() error {
		return stateStore.AcknowledgeReport(ctx, reportID)
	})
}

func (c *Client) finalizeReleasedProcessesLoop(ctx context.Context) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := c.finalizeReleasedProcesses(ctx); err != nil &&
			ctx.Err() == nil {
			c.log.Warn(
				"finalize released process failed",
				"error",
				err,
			)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (c *Client) finalizeReleasedProcesses(ctx context.Context) error {
	stateStore, err := c.stateStore(ctx)
	if err != nil {
		return err
	}
	for _, runtime := range c.cleanupProcesses() {
		process, found, err := stateStore.Process(ctx, runtime.processID)
		if err != nil {
			continue
		}
		if !found {
			if err := c.removeProcessRuntimeArtifacts(
				runtime.processID,
				runtime.supervisorInstanceID,
				true,
			); err != nil {
				continue
			}
			c.removeProcessInstance(
				runtime.processID,
				runtime.supervisorInstanceID,
			)
			continue
		}
		var cleanupPending bool
		if process.Phase == statedb.ProcessPreparing ||
			process.Phase == statedb.ProcessPrepared {
			cleanupPending, err = c.closeRejectedPreparation(
				ctx,
				runtime.processID,
				runtime.supervisorInstanceID,
				nil,
				nil,
			)
		} else {
			cleanupPending, err = c.closeStorageExhaustedProcess(ctx, runtime)
		}
		if cleanupPending {
			continue
		}
		if err != nil {
			c.log.Debug(
				"process cleanup failed",
				"process_id",
				runtime.processID,
				"supervisor_instance_id",
				runtime.supervisorInstanceID,
				"error",
				err,
			)
			continue
		}
		c.removeProcessInstance(
			runtime.processID,
			runtime.supervisorInstanceID,
		)
	}

	processes, err := stateStore.Processes(ctx)
	if err != nil {
		return err
	}
	for _, process := range processes {
		if !process.ServerReleased {
			continue
		}
		if !process.LocalClosed {
			if runtime, ok := c.localProcess(process.ProcessID); ok {
				if runtime.cleanupOnly || runtime.runner == nil {
					continue
				}
				probeCtx, cancel := context.WithTimeout(
					ctx,
					250*time.Millisecond,
				)
				probeErr := runtime.runner.Status(probeCtx)
				cancel()
				if probeErr == nil {
					continue
				}
				c.removeProcessInstance(
					process.ProcessID,
					process.SupervisorInstanceID,
				)
			}
			machine, machineErr := c.machineStore()
			if machineErr != nil {
				return machineErr
			}
			lockPath, pathErr := machine.LifetimeLockPath(
				process.ProcessID,
			)
			if pathErr != nil {
				return pathErr
			}
			lock, lockErr := localstore.TryAcquireExistingLock(lockPath)
			switch {
			case lockErr == nil:
				recoverErr := c.recoverStoppedReleasedProcess(
					ctx,
					process.ProcessID,
					process.SupervisorInstanceID,
					lock,
				)
				releaseErr := lock.Release()
				if recoverErr != nil {
					continue
				}
				if releaseErr != nil {
					return releaseErr
				}
			case errors.Is(lockErr, localstore.ErrLockHeld):
				continue
			case !errors.Is(lockErr, os.ErrNotExist):
				return lockErr
			}
		}

		current, found, err := stateStore.Process(ctx, process.ProcessID)
		if err != nil {
			return err
		}
		if !found || !current.LocalClosed || !current.ServerReleased {
			continue
		}
		actions, err := stateStore.Actions(ctx, current.ProcessID)
		if err != nil {
			return err
		}
		if len(actions) != 0 {
			continue
		}
		reports, err := stateStore.ReportsForProcess(ctx, current.ProcessID)
		if err != nil {
			return err
		}
		settled := true
		for _, report := range reports {
			if report.State != statedb.ReportAcknowledged {
				settled = false
				break
			}
		}
		if !settled {
			continue
		}
		if err := c.releaseProcessRuntimeArtifacts(
			current.ProcessID,
			current.SupervisorInstanceID,
		); err != nil {
			if errors.Is(err, localstore.ErrLockHeld) {
				continue
			}
			return err
		}
		if err := stateStore.DeleteClosedAfterArtifacts(
			ctx,
			current.ProcessID,
			current.SupervisorInstanceID,
		); err != nil {
			return err
		}
		c.removeProcessInstance(
			current.ProcessID,
			current.SupervisorInstanceID,
		)
	}
	return nil
}
