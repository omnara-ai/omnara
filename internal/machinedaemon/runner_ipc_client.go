package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
)

type ipcProcessRunner struct {
	endpoint             string
	supervisorToken      string
	supervisorInstanceID string
	done                 chan struct{}
	doneOnce             sync.Once

	reconciliationMu sync.RWMutex
	reconciliation   *runnerReconciliationSession
}

type runnerReconciliationSession struct {
	conn net.Conn
	mu   sync.Mutex
}

func (r *ipcProcessRunner) BeginReconciliation(
	ctx context.Context,
) error {
	r.reconciliationMu.Lock()
	defer r.reconciliationMu.Unlock()
	if r.reconciliation != nil {
		return errors.New(
			"supervisor reconciliation is already active",
		)
	}
	conn, err := localipc.Dial(ctx, r.endpoint)
	if err != nil {
		return err
	}
	request := runnerRequest{
		SupervisorToken:      r.supervisorToken,
		SupervisorInstanceID: r.supervisorInstanceID,
		Method:               runnerMethodBeginReconciliation,
	}
	if err := writeRunnerMessage(ctx, conn, request); err != nil {
		_ = conn.Close()
		return err
	}
	if _, err := readRunnerResponse(ctx, conn); err != nil {
		_ = conn.Close()
		return err
	}
	r.reconciliation = &runnerReconciliationSession{conn: conn}
	return nil
}

func (r *ipcProcessRunner) EndReconciliation() error {
	r.reconciliationMu.Lock()
	defer r.reconciliationMu.Unlock()
	if r.reconciliation == nil {
		return nil
	}
	err := r.reconciliation.conn.Close()
	r.reconciliation = nil
	return err
}

func (r *ipcProcessRunner) Status(
	ctx context.Context,
) error {
	_, err := r.call(
		ctx,
		runnerRequest{Method: runnerMethodStatus},
	)
	return err
}

func (r *ipcProcessRunner) StartOnce(
	ctx context.Context,
) error {
	response, err := r.call(
		ctx,
		runnerRequest{
			Method:  runnerMethodStartOnce,
			Payload: json.RawMessage(`{}`),
		},
	)
	if err != nil {
		return err
	}
	switch response.StartOutcome {
	case processStarted, processNotStarted, processAmbiguous:
		return nil
	default:
		return errors.New(
			"supervisor returned an invalid start outcome",
		)
	}
}

func (r *ipcProcessRunner) ApplyOnce(
	ctx context.Context,
	action ProcessAction,
) error {
	body, err := json.Marshal(action)
	if err != nil {
		return err
	}
	_, err = r.call(
		ctx,
		runnerRequest{Method: runnerMethodApplyOnce, Payload: body},
	)
	return err
}

func (r *ipcProcessRunner) CloseUngranted(ctx context.Context) error {
	_, err := r.call(
		ctx,
		runnerRequest{
			Method:  runnerMethodCloseUngranted,
			Payload: json.RawMessage(`{}`),
		},
	)
	return err
}

func (r *ipcProcessRunner) Terminate(
	ctx context.Context,
	reason string,
) error {
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		return err
	}
	_, err = r.call(
		ctx,
		runnerRequest{Method: runnerMethodTerminate, Payload: body},
	)
	return err
}

func (r *ipcProcessRunner) Done() <-chan struct{} {
	return r.done
}

func (r *ipcProcessRunner) IsDone() bool {
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

func (r *ipcProcessRunner) markDone() {
	r.doneOnce.Do(func() { close(r.done) })
}

func (r *ipcProcessRunner) waitReady(ctx context.Context) error {
	deadline := time.Now().Add(10 * time.Second)
	for {
		callCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := r.Status(callCtx)
		cancel()
		if err == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			return err
		}
		if err := sleepContext(ctx, 50*time.Millisecond); err != nil {
			return err
		}
	}
}

func (r *ipcProcessRunner) call(
	ctx context.Context,
	request runnerRequest,
) (response runnerResponse, err error) {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
	}
	if request.SupervisorToken == "" {
		request.SupervisorToken = r.supervisorToken
	}
	if request.SupervisorInstanceID == "" {
		request.SupervisorInstanceID = r.supervisorInstanceID
	}
	r.reconciliationMu.RLock()
	if session := r.reconciliation; session != nil {
		defer r.reconciliationMu.RUnlock()
		return session.call(ctx, request)
	}
	r.reconciliationMu.RUnlock()
	conn, err := localipc.Dial(ctx, r.endpoint)
	if err != nil {
		return runnerResponse{}, err
	}
	defer func() {
		if closeErr := conn.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	if err := writeRunnerMessage(ctx, conn, request); err != nil {
		return runnerResponse{}, err
	}
	return readRunnerResponse(ctx, conn)
}

func (s *runnerReconciliationSession) call(
	ctx context.Context,
	request runnerRequest,
) (runnerResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := writeRunnerMessage(ctx, s.conn, request); err != nil {
		_ = s.conn.Close()
		return runnerResponse{}, err
	}
	var response runnerResponse
	if err := readRunnerMessage(ctx, s.conn, &response); err != nil {
		_ = s.conn.Close()
		return runnerResponse{}, err
	}
	return response, runnerResponseError(response)
}
