package notifications

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestTxNotificationsSnapshotOwnsNestedValues(t *testing.T) {
	n := NewTxNotifications()
	machine, process, runtime, agent, worker := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	n.AddDaemonWork(machine)
	n.AddDaemonProcessTermination(machine, process)
	n.AddDaemonRuntimeEnded(runtime, machine, DaemonRuntimeEndReconnect)
	n.AddAgentEvent(agent, 1, "agent_input")
	n.AddToolCallUpdate(agent, uuid.New(), "waiting", &AgentInteractionUpdate{ID: uuid.New(), State: "open"})
	n.AddWorkerControlCancel(worker, agent, runtime)
	snapshot := n.Clone()
	expected := snapshot.Clone()
	delete(n.daemonWorkByMachine, machine)
	delete(n.processTerminationByMachine[machine], process)
	delete(n.runtimeEndedByID, runtime)
	n.agentEventByID[agent][0].Sequence = 2
	n.toolCallUpdates[0].InteractionUpdate.State = "resolved"
	n.workerControls[0].Control.Cancel.RuntimeLockID = uuid.New()
	require.Equal(t, expected, snapshot)
	n.Restore(snapshot)
	require.Equal(t, expected, n)
	snapshot.toolCallUpdates[0].InteractionUpdate.State = "canceled"
	snapshot.agentEventByID[agent][0].Sequence = 3
	delete(snapshot.processTerminationByMachine[machine], process)
	require.Equal(t, expected, n)
}
