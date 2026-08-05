package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/coder/websocket"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (s *daemonSocket) handleProcessAccept(ctx context.Context, msg daemonprotocol.Message) error {
	processID, err := parsePublicID(publicid.KindProcess, msg.ProcessID)
	if err != nil {
		return err
	}
	offer, found, err := s.server.store.Execution().AcceptDaemonProcess(
		ctx,
		executionstore.AcceptDaemonProcessInput{
			Authority: s.authority(),
			ProcessID: processID,
		},
	)
	if err != nil {
		return err
	}
	if !found {
		s.recordSocketEvent("process_accept", "conflict", "not_found")
		return errDaemonProcessOfferUnavailable
	}
	processPublicID, err := publicID(publicid.KindProcess, offer.Process.ID)
	if err != nil {
		return err
	}
	s.markAcceptedProcess(offer.Process.ID)
	s.enqueueOrClose(daemonprotocol.Message{
		Type:      daemonprotocol.MessageProcessAcceptAck,
		ProcessID: processPublicID,
	})
	s.enqueueDrain(ctx)
	return nil
}

func (s *daemonSocket) handleActionAccept(ctx context.Context, msg daemonprotocol.Message) error {
	processID, err := parsePublicID(publicid.KindProcess, msg.ProcessID)
	if err != nil {
		return err
	}
	actionID, err := parsePublicID(publicid.KindProcessAction, msg.ProcessActionID)
	if err != nil {
		return err
	}
	grant, found, err := s.server.store.Execution().AcceptDaemonProcessAction(
		ctx,
		executionstore.AcceptDaemonProcessActionInput{
			Authority: s.authority(),
			ProcessID: processID,
			ID:        actionID,
		},
	)
	if err != nil {
		return err
	}
	if !found {
		s.recordSocketEvent("action_accept", "conflict", "not_found")
		return errDaemonActionOfferUnavailable
	}
	actionPublicID, err := publicID(publicid.KindProcessAction, grant.Action.ID)
	if err != nil {
		return err
	}
	processPublicID, err := publicID(publicid.KindProcess, grant.Action.ProcessID)
	if err != nil {
		return err
	}
	s.markAcceptedAction(grant.Action.ID)
	s.enqueueOrClose(
		daemonprotocol.Message{
			Type:            daemonprotocol.MessageActionAcceptAck,
			ProcessID:       processPublicID,
			ProcessActionID: actionPublicID,
			ActionGrant: &daemonprotocol.ActionGrant{
				ProcessID:           processPublicID,
				ProcessActionID:     actionPublicID,
				ProcessState:        daemonprotocol.ProcessState(grant.ProcessState),
				DefaultOutputCursor: grant.DefaultOutputCursor,
			},
		},
	)
	s.enqueueDrain(ctx)
	return nil
}

func (s *daemonSocket) handleReport(ctx context.Context, msg daemonprotocol.Message) error {
	if msg.ReportID == "" {
		return fmt.Errorf("%w: report_id is required", errDaemonReportValidation)
	}
	if msg.Event == nil {
		return fmt.Errorf("%w: event is required", errDaemonReportValidation)
	}
	body := *msg.Event
	if err := validateDaemonReportedEvent(body); err != nil {
		return err
	}
	missingErr := errors.New(
		"daemon process does not belong to this machine",
	)
	cleanupOnly, err := s.server.applyDaemonReportedEventForMachineWithContext(
		ctx,
		s.authority(),
		body,
		missingErr,
	)
	if err != nil {
		s.recordSocketEvent("report", "error", daemonErrorCode(err))
		return err
	}
	s.forgetAcceptedWork(msg.Event)
	ackStatus := daemonprotocol.AckStatusCommitted
	if cleanupOnly {
		ackStatus = daemonprotocol.AckStatusCleanupOnly
	}
	s.enqueueOrClose(
		daemonprotocol.Message{
			Type:      daemonprotocol.MessageReportAck,
			ReportID:  msg.ReportID,
			AckStatus: ackStatus,
		},
	)
	s.recordSocketEvent("report", string(ackStatus), "none")
	s.enqueueDrain(ctx)
	return nil
}

func (s *daemonSocket) drainLoop(ctx context.Context) {
	drainCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := make(chan struct{})
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-stop:
		}
	}()
	defer close(stop)
	for {
		if err := drainCtx.Err(); err != nil {
			s.workMu.Lock()
			s.drainQueued = false
			s.drainRunning = false
			s.workMu.Unlock()
			return
		}
		s.workMu.Lock()
		s.drainQueued = false
		s.drainRunning = true
		s.workMu.Unlock()

		s.drainWork(drainCtx)

		s.workMu.Lock()
		if s.drainQueued {
			s.workMu.Unlock()
			continue
		}
		s.drainRunning = false
		s.workMu.Unlock()
		return
	}
}

func (s *daemonSocket) drainWork(ctx context.Context) {
	processOffers, err := s.server.store.Execution().ListDaemonProcessOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: s.authority(),
			Limit:     daemonProcessOfferLimit,
		},
	)
	if err != nil {
		_ = s.enqueue(daemonprotocol.Message{
			Type:  daemonprotocol.MessageError,
			Error: err.Error(),
		})
		return
	}
	for _, offer := range processOffers {
		if offer.RetryError != nil {
			_ = s.enqueue(daemonprotocol.Message{
				Type:  daemonprotocol.MessageError,
				Error: offer.RetryError.Error(),
			})
			continue
		}
		if s.processAcceptedOnConnection(offer.Process.ID) {
			continue
		}
		processID, err := publicID(publicid.KindProcess, offer.Process.ID)
		if err != nil {
			continue
		}
		if !s.enqueue(daemonProcessOfferMessage(processID, offer)) {
			s.recordSocketEvent("send_queue", "dropped", "queue_full")
			s.close(websocket.StatusPolicyViolation, "daemon socket send queue full")
			return
		}
	}
	actionOffers, err := s.server.store.Execution().ListDaemonProcessActionOffers(
		ctx,
		executionstore.DaemonWorkInput{
			Authority: s.authority(),
			Limit:     daemonActionOfferLimit,
		},
	)
	if err != nil {
		_ = s.enqueue(daemonprotocol.Message{
			Type:  daemonprotocol.MessageError,
			Error: err.Error(),
		})
		return
	}
	for _, action := range actionOffers {
		if s.actionAcceptedOnConnection(action.ID) {
			continue
		}
		processID, err := publicID(publicid.KindProcess, action.ProcessID)
		if err != nil {
			continue
		}
		actionID, err := publicID(publicid.KindProcessAction, action.ID)
		if err != nil {
			continue
		}
		if !s.enqueue(
			daemonprotocol.Message{
				Type:            daemonprotocol.MessageActionOffer,
				ProcessID:       processID,
				ProcessActionID: actionID,
				ActionOffer: &daemonprotocol.ActionOffer{
					ProcessID:       processID,
					ProcessActionID: actionID,
					ActionKind:      action.ActionKind,
					Seq:             action.Seq,
					Payload:         action.Payload,
				},
			},
		) {
			s.recordSocketEvent("send_queue", "dropped", "queue_full")
			s.close(websocket.StatusPolicyViolation, "daemon socket send queue full")
			return
		}
	}
}

func daemonProcessOfferMessage(processID string, offer executionstore.DaemonProcessOffer) daemonprotocol.Message {
	processOffer := &daemonprotocol.ProcessOffer{
		ProcessID:        processID,
		PreparationError: offer.PreparationError,
	}
	if processOffer.PreparationError == "" {
		processOffer = &daemonprotocol.ProcessOffer{
			ProcessID:      processID,
			IOMode:         offer.Process.IOMode,
			Command:        offer.Process.Command,
			ShellSelector:  offer.Process.ShellSelector,
			Cwd:            offer.Process.Cwd,
			Env:            offer.Env,
			WaitMs:         offer.Process.InitialWaitMS,
			TimeoutSeconds: offer.Process.TimeoutSeconds,
		}
	}
	message := daemonprotocol.Message{
		Type:         daemonprotocol.MessageProcessOffer,
		ProcessID:    processID,
		ProcessOffer: processOffer,
	}
	encoded, err := json.Marshal(message)
	if err != nil {
		message.ProcessOffer = &daemonprotocol.ProcessOffer{
			ProcessID:        processID,
			PreparationError: "process offer could not be encoded",
		}
	} else if len(encoded) > daemonSocketReadLimitBytes {
		message.ProcessOffer = &daemonprotocol.ProcessOffer{
			ProcessID:        processID,
			PreparationError: "process offer exceeds daemon message size limit",
		}
	}
	return message
}

func (s *daemonSocket) markAcceptedProcess(processID storage.ID) {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	s.acceptedProcesses[processID] = struct{}{}
}

func (s *daemonSocket) markAcceptedAction(actionID storage.ID) {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	s.acceptedActions[actionID] = struct{}{}
}

func (s *daemonSocket) processAcceptedOnConnection(processID storage.ID) bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	_, ok := s.acceptedProcesses[processID]
	return ok
}

func (s *daemonSocket) actionAcceptedOnConnection(actionID storage.ID) bool {
	s.workMu.Lock()
	defer s.workMu.Unlock()
	_, ok := s.acceptedActions[actionID]
	return ok
}

func (s *daemonSocket) forgetAcceptedWork(event *daemonprotocol.ReportedEvent) {
	if event == nil {
		return
	}
	processID, processErr := parsePublicID(publicid.KindProcess, event.ProcessID)
	var actionID storage.ID
	var actionErr error
	if event.ProcessActionID != "" {
		actionID, actionErr = parsePublicID(publicid.KindProcessAction, event.ProcessActionID)
	}
	s.workMu.Lock()
	defer s.workMu.Unlock()
	switch event.Type {
	case daemonprotocol.EventProcessStarted,
		daemonprotocol.EventProcessFinished:
		if processErr == nil {
			delete(s.acceptedProcesses, processID)
		}
	case daemonprotocol.EventProcessActionApplied,
		daemonprotocol.EventProcessActionFailed,
		daemonprotocol.EventProcessActionUnknown:
		if actionErr == nil {
			delete(s.acceptedActions, actionID)
		}
	}
}
