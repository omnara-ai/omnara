package machinedaemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

const (
	runnerIPCMaxFrameBytes = 2 * 1024 * 1024
	runnerIPCWriteTimeout  = 15 * time.Second
)

type runnerMethod string

const (
	runnerMethodBeginReconciliation runnerMethod = "begin_reconciliation"
	runnerMethodStatus              runnerMethod = "status"
	runnerMethodPrepare             runnerMethod = "prepare"
	runnerMethodStartOnce           runnerMethod = "start_once"
	runnerMethodApplyOnce           runnerMethod = "apply_once"
	runnerMethodCloseUngranted      runnerMethod = "close_ungranted"
	runnerMethodTerminate           runnerMethod = "terminate"
)

type runnerErrorCode string

const (
	runnerErrorActionBlocked              runnerErrorCode = "action_blocked"
	runnerErrorSupervisorIdentityMismatch runnerErrorCode = "supervisor_identity_mismatch"
)

var errRunnerIdentityMismatch = errors.New("supervisor identity mismatch")

type runnerStage string

const (
	runnerStageActionCommitted   runnerStage = "action_committed"
	runnerStageFenceWriterParked runnerStage = "fence_writer_parked"
)

type runnerRequest struct {
	SupervisorToken      string          `json:"supervisor_token,omitempty"`
	SupervisorInstanceID string          `json:"supervisor_instance_id,omitempty"`
	Method               runnerMethod    `json:"method"`
	Payload              json.RawMessage `json:"payload,omitempty"`
	NotifyStages         bool            `json:"notify_stages,omitempty"`
}

type runnerResponse struct {
	OK           bool                `json:"ok"`
	StartOutcome processStartOutcome `json:"start_outcome,omitempty"`
	Error        string              `json:"error,omitempty"`
	ErrorCode    runnerErrorCode     `json:"error_code,omitempty"`
	Stage        runnerStage         `json:"stage,omitempty"`
	ActionID     string              `json:"action_id,omitempty"`
}

func writeRunnerMessage(
	ctx context.Context,
	conn net.Conn,
	value any,
) error {
	if conn == nil {
		return errors.New("runner connection is nil")
	}
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if len(body) > runnerIPCMaxFrameBytes {
		return fmt.Errorf(
			"runner message exceeds %d-byte limit",
			runnerIPCMaxFrameBytes,
		)
	}
	stop := interruptConnOnContext(ctx, conn)
	defer stop()
	deadline := time.Now().Add(runnerIPCWriteTimeout)
	if contextDeadline, ok := ctx.Deadline(); ok {
		deadline = contextDeadline
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		return err
	}
	for len(body) > 0 {
		written, writeErr := conn.Write(body)
		if written > 0 {
			body = body[written:]
		}
		if writeErr != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return writeErr
		}
		if written == 0 {
			return io.ErrUnexpectedEOF
		}
	}
	return nil
}

func readRunnerResponse(
	ctx context.Context,
	conn net.Conn,
) (runnerResponse, error) {
	var response runnerResponse
	if err := readRunnerMessage(ctx, conn, &response); err != nil {
		return response, err
	}
	return response, runnerResponseError(response)
}

func runnerResponseError(response runnerResponse) error {
	if !response.OK {
		if response.Error == "" {
			return errors.New("supervisor request failed")
		}
		if response.ErrorCode == runnerErrorActionBlocked {
			return fmt.Errorf(
				"%w: %s",
				statedb.ErrActionBlocked,
				response.Error,
			)
		}
		if response.ErrorCode == runnerErrorSupervisorIdentityMismatch {
			return fmt.Errorf(
				"%w: %s",
				errRunnerIdentityMismatch,
				response.Error,
			)
		}
		return errors.New(response.Error)
	}
	return nil
}

type runnerFrameReader struct {
	conn    net.Conn
	limited *io.LimitedReader
	reader  *bufio.Reader
}

func newRunnerFrameReader(conn net.Conn) *runnerFrameReader {
	limited := &io.LimitedReader{R: conn}
	return &runnerFrameReader{
		conn:    conn,
		limited: limited,
		reader:  bufio.NewReader(limited),
	}
}

func (r *runnerFrameReader) read(
	ctx context.Context,
	out *runnerResponse,
) error {
	if r == nil || r.conn == nil {
		return errors.New("runner connection is nil")
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := r.conn.SetReadDeadline(deadline); err != nil {
			return err
		}
	}
	stop := interruptConnOnContext(ctx, r.conn)
	defer stop()
	r.limited.N = runnerIPCMaxFrameBytes + 1
	line, err := r.reader.ReadBytes('\n')
	if len(line) > runnerIPCMaxFrameBytes {
		return fmt.Errorf(
			"runner message exceeds %d-byte limit",
			runnerIPCMaxFrameBytes,
		)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return json.Unmarshal(line, out)
}

func (r *runnerFrameReader) response(
	ctx context.Context,
	onStage func(runnerResponse),
) (runnerResponse, error) {
	for {
		var response runnerResponse
		if err := r.read(ctx, &response); err != nil {
			return runnerResponse{}, err
		}
		if response.Stage == "" {
			return response, nil
		}
		if onStage != nil {
			onStage(response)
		}
	}
}

func readRunnerMessage(
	ctx context.Context,
	conn net.Conn,
	out any,
) error {
	if conn == nil {
		return errors.New("runner connection is nil")
	}
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetReadDeadline(deadline); err != nil {
			return err
		}
	}
	stop := interruptConnOnContext(ctx, conn)
	defer stop()
	limited := &io.LimitedReader{R: conn, N: runnerIPCMaxFrameBytes + 1}
	line, err := bufio.NewReader(limited).ReadBytes('\n')
	if len(line) > runnerIPCMaxFrameBytes {
		return fmt.Errorf(
			"runner message exceeds %d-byte limit",
			runnerIPCMaxFrameBytes,
		)
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	return json.Unmarshal(line, out)
}

func interruptConnOnContext(
	ctx context.Context,
	conn net.Conn,
) func() {
	if ctx == nil {
		return func() {}
	}
	stop := context.AfterFunc(ctx, func() {
		_ = conn.SetDeadline(time.Now())
	})
	return func() {
		stop()
	}
}
