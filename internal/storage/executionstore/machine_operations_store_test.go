package executionstore

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

func TestMachineOperationDisposition(t *testing.T) {
	cases := []struct {
		name  string
		state MachineOperationState
		claim ProcessReconciliationClaim
		want  daemonprotocol.ProcessDisposition
	}{
		{"queued preparing", MachineOperationStateQueued,
			ProcessReconciliationClaim{Phase: daemonprotocol.ProcessPhasePreparing},
			daemonprotocol.ProcessDispositionClosePreparation},
		{"terminal prepared", MachineOperationStateFailed,
			ProcessReconciliationClaim{Phase: daemonprotocol.ProcessPhasePrepared},
			daemonprotocol.ProcessDispositionClosePreparation},
		{"terminal committed", MachineOperationStateExited,
			ProcessReconciliationClaim{Phase: daemonprotocol.ProcessPhaseTerminal, ExecutionCommitted: true},
			daemonprotocol.ProcessDispositionRelease},
		{"running with terminal report", MachineOperationStateRunning,
			ProcessReconciliationClaim{Phase: daemonprotocol.ProcessPhaseTerminal, ExecutionCommitted: true},
			daemonprotocol.ProcessDispositionRetain},
		{"running live", MachineOperationStateRunning,
			ProcessReconciliationClaim{
				Phase: daemonprotocol.ProcessPhaseAccepted, ExecutionCommitted: true, SupervisorLive: true,
			},
			daemonprotocol.ProcessDispositionRetain},
		{"running lost", MachineOperationStateRunning,
			ProcessReconciliationClaim{Phase: daemonprotocol.ProcessPhaseAccepted, ExecutionCommitted: true},
			daemonprotocol.ProcessDispositionRelease},
		{"starting prepared live", MachineOperationStateStarting,
			ProcessReconciliationClaim{Phase: daemonprotocol.ProcessPhasePrepared, SupervisorLive: true},
			daemonprotocol.ProcessDispositionStart},
	}
	for _, tc := range cases {
		got := machineOperationDisposition(MachineOperationRecord{State: tc.state}, tc.claim)
		if got != tc.want {
			t.Errorf("%s: disposition = %q, want %q", tc.name, got, tc.want)
		}
	}
}
