package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func TestRunnerProtocolRoundTrip(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		var request runnerRequest
		if err := readRunnerMessage(context.Background(), server, &request); err != nil {
			done <- err
			return
		}
		var action ProcessAction
		if err := json.Unmarshal(request.Payload, &action); err != nil {
			done <- err
			return
		}
		if request.Method != "apply_once" || action.ID != "pda_1" {
			done <- unexpectedRunnerRequestError(
				string(request.Method) + "/" + action.ID,
			)
			return
		}
		done <- writeRunnerMessage(
			context.Background(),
			server,
			runnerResponse{OK: true},
		)
	}()
	actionBody, err := json.Marshal(ProcessAction{ID: "pda_1"})
	if err != nil {
		t.Fatalf("marshal action: %v", err)
	}
	if err := writeRunnerMessage(
		context.Background(),
		client,
		runnerRequest{Method: "apply_once", Payload: actionBody},
	); err != nil {
		t.Fatalf("write request: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := readRunnerResponse(ctx, client)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if !response.OK {
		t.Fatalf("response = %+v", response)
	}
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}

func TestRunnerProtocolPreservesActionBlockedError(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()
	go func() {
		_ = writeRunnerMessage(
			context.Background(),
			server,
			runnerResponse{
				OK:        false,
				Error:     "waiting for action sequence 1",
				ErrorCode: "action_blocked",
			},
		)
	}()
	_, err := readRunnerResponse(context.Background(), client)
	if !errors.Is(err, statedb.ErrActionBlocked) {
		t.Fatalf("runner error = %v, want action blocked", err)
	}
}

func TestRunnerProtocolPreservesStorageErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		code runnerErrorCode
		want error
	}{
		{runnerErrorStorageExhaustion, errRunnerStorageExhaustion},
		{runnerErrorStorageExhaustionReady, errStorageExhaustionTerminalReady},
	} {
		err := runnerResponseError(runnerResponse{
			Error:     "storage failure",
			ErrorCode: test.code,
		})
		if !errors.Is(err, test.want) {
			t.Fatalf("runner error = %v, want %v", err, test.want)
		}
	}
}

type unexpectedRunnerRequestError string

func (e unexpectedRunnerRequestError) Error() string {
	return "unexpected runner request: " + string(e)
}
