package e2b

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

func TestObserveRuntimeStates(t *testing.T) {
	installationID := uuid.New()
	running, paused, foreign, gone, duplicate := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	a := &fakeAPI{}
	a.addSandbox("sbx-running", mustMetadata(t, installationID, running), 2, 2048)
	pausedSandbox := a.addSandbox("sbx-paused", mustMetadata(t, installationID, paused), 2, 2048)
	pausedSandbox.State = sandboxStatePaused
	a.sandboxes["sbx-paused"] = pausedSandbox
	a.addSandbox("sbx-foreign", mustMetadata(t, installationID, uuid.New()), 2, 2048)
	targets := []providers.RuntimeTarget{
		{InstallationID: installationID, MachineID: running, ProviderResourceID: "sbx-running"},
		{InstallationID: installationID, MachineID: paused, ProviderResourceID: "sbx-paused"},
		{InstallationID: installationID, MachineID: foreign, ProviderResourceID: "sbx-foreign"},
		{InstallationID: installationID, MachineID: gone, ProviderResourceID: "sbx-gone"},
		{InstallationID: installationID, MachineID: duplicate, ProviderResourceID: "sbx-dup"},
		{InstallationID: installationID, MachineID: duplicate, ProviderResourceID: "sbx-dup"},
		{InstallationID: installationID, MachineID: uuid.New(), ProviderResourceID: " sbx-running"},
	}
	observations, err := testProvider(a).ObserveRuntimeStates(t.Context(), targets)
	require.NoError(t, err)
	states := make([]providers.RuntimeState, len(observations))
	for index, observation := range observations {
		states[index] = observation.State
	}
	require.Equal(t, []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateTerminated,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
	}, states)
}
