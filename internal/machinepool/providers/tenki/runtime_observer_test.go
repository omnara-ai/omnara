package tenki

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/stretchr/testify/require"
)

func TestNormalizeRuntimeState(t *testing.T) {
	for state, want := range map[string]providers.RuntimeState{
		sessionStateRunning:      providers.RuntimeStateRunning,
		sessionStatePaused:       providers.RuntimeStateInactive,
		sessionStateUserShutdown: providers.RuntimeStateInactive,
		sessionStateCreating:     providers.RuntimeStateTransitional,
		sessionStatePausing:      providers.RuntimeStateTransitional,
		sessionStateResuming:     providers.RuntimeStateTransitional,
		sessionStateTerminating:  providers.RuntimeStateTransitional,
		sessionStateTerminated:   providers.RuntimeStateTerminated,
		"FUTURE_STATE":           providers.RuntimeStateUnknown,
	} {
		require.Equal(t, want, normalizeRuntimeState(state), state)
	}
}

func TestRuntimeObservationTreatsUncertainResultsConservatively(t *testing.T) {
	installationID, machineID := uuid.New(), uuid.New()
	target := providers.RuntimeTarget{
		InstallationID:     installationID,
		MachineID:          machineID,
		ProviderResourceID: "session",
	}
	a := &fakeAPI{}
	p := testProvider(a)
	bulk, err := p.ObserveRuntimeStates(t.Context(), []providers.RuntimeTarget{target})
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateTerminated, bulk[0].State)
	exact, err := p.ObserveRuntimeState(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateTerminated, exact.State)
	a.sessions = []session{ownedSession(t, "session", sessionStateRunning, installationID, machineID)}
	bulk, err = p.ObserveRuntimeStates(t.Context(), []providers.RuntimeTarget{target, target})
	require.NoError(t, err)
	for _, observation := range bulk {
		require.Equal(t, providers.RuntimeStateUnknown, observation.State)
	}
	bulk, err = p.ObserveRuntimeStates(t.Context(), []providers.RuntimeTarget{target})
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateRunning, bulk[0].State)
	exact, err = p.ObserveRuntimeState(t.Context(), target)
	require.NoError(t, err)
	require.Equal(t, providers.RuntimeStateRunning, exact.State)
}
