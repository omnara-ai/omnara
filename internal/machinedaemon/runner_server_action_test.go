package machinedaemon

import (
	"context"
	"os"
	"os/exec"
	"testing"
)

func TestTerminateAfterNaturalExitReturnsAlreadyStopped(t *testing.T) {
	terminalReady := make(chan struct{})
	close(terminalReady)
	state := runnerServerState{
		bootstrap: supervisorIdentityBootstrap{ProcessID: "prc_stopped"},
		prepared: &localProcessRunner{
			cmd:                 &exec.Cmd{Process: &os.Process{Pid: 1}},
			terminalResultReady: terminalReady,
		},
	}
	result := state.applyLiveAction(
		context.Background(),
		ProcessAction{ActionKind: "terminate"},
	)
	if result.EventType != "process_action_applied" {
		t.Fatalf("terminate result = %+v", result)
	}
	if result.StateReasonCode != "already_stopped" {
		t.Fatalf("terminate reason = %q", result.StateReasonCode)
	}
	if len(result.Result) != 0 {
		t.Fatalf("daemon supplied canonical server result: %s", result.Result)
	}
}
