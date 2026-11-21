package docker

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

func TestDockerRuntimeObservationMapsContainerStates(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	states := []string{"running", "paused", "restarting", "exited"}
	want := []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
		providers.RuntimeStateTransitional,
		providers.RuntimeStateTerminated,
		providers.RuntimeStateTerminated,
	}
	var targets []providers.RuntimeTarget
	for _, state := range states {
		machineID := uuid.New()
		_, err := provision(t, api, installationID, machineID)
		require.NoError(t, err)
		name, _, err := machineIdentity(installationID, machineID)
		require.NoError(t, err)
		current := api.containers[name]
		current.State.Status = state
		api.containers[name] = current
		targets = append(targets, providers.RuntimeTarget{
			InstallationID: installationID, MachineID: machineID, ProviderResourceID: name,
		})
	}
	goneMachineID := uuid.New()
	goneName, _, err := machineIdentity(installationID, goneMachineID)
	require.NoError(t, err)
	targets = append(targets, providers.RuntimeTarget{
		InstallationID: installationID, MachineID: goneMachineID, ProviderResourceID: goneName,
	})

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	require.NoError(t, err)
	for index, observation := range observations {
		require.Equal(t, targets[index].MachineID, observation.MachineID)
		require.Equal(t, want[index], observation.State, "target %d", index)
		exact, err := newTestProvider(api).ObserveRuntimeState(context.Background(), targets[index])
		require.NoError(t, err)
		require.Equal(t, want[index], exact.State, "exact target %d", index)
	}
}

func TestDockerRuntimeObservationListsOnlyRequestedMachinesInBatches(t *testing.T) {
	api := newFakeAPI()
	installationID := uuid.New()
	running := func(machineID uuid.UUID) string {
		name, labels, err := machineIdentity(installationID, machineID)
		require.NoError(t, err)
		api.containers[name] = container{State: containerState{Status: "running"}, Config: containerConfig{Labels: labels}}
		return name
	}
	var targets []providers.RuntimeTarget
	for range runtimeListNameBatch + 1 {
		machineID := uuid.New()
		targets = append(targets, providers.RuntimeTarget{
			InstallationID: installationID, MachineID: machineID, ProviderResourceID: running(machineID),
		})
	}
	unrequested := running(uuid.New())

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	require.NoError(t, err)
	for _, observation := range observations {
		require.Equal(t, providers.RuntimeStateRunning, observation.State)
	}
	require.Equal(t, 2, api.listCalls)
	require.NotContains(t, api.listedNames, unrequested)
}
