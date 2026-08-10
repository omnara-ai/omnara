package providercontract

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

const (
	runtimeObservationPollInterval = 500 * time.Millisecond
	runtimeObservationTimeout      = 30 * time.Second
)

func WaitForPresentRuntimeObservation(
	t testing.TB,
	ctx context.Context,
	target providers.RuntimeTarget,
	observe func() (providers.RuntimeObservation, error),
) providers.RuntimeObservation {
	t.Helper()
	deadline := time.Now().Add(runtimeObservationTimeout)
	var lastObservation providers.RuntimeObservation
	var lastErr error
	for {
		lastObservation, lastErr = observe()
		if lastErr == nil && runtimeObservationMatches(
			target,
			lastObservation,
			providers.RuntimeStateRunning,
			providers.RuntimeStateInactive,
		) {
			return lastObservation
		}
		if time.Now().After(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf(
				"wait for provider runtime observation: %v; last error: %v; last observation: %+v",
				ctx.Err(),
				lastErr,
				lastObservation,
			)
		case <-time.After(runtimeObservationPollInterval):
		}
	}
	t.Fatalf(
		"provider runtime observation did not settle; last error: %v; last observation: %+v",
		lastErr,
		lastObservation,
	)
	return providers.RuntimeObservation{}
}

func AssertRuntimeObservation(
	t testing.TB,
	target providers.RuntimeTarget,
	observation providers.RuntimeObservation,
	wantStates ...providers.RuntimeState,
) {
	t.Helper()
	if !runtimeObservationMatches(target, observation, wantStates...) {
		t.Fatalf("unexpected runtime observation: %+v", observation)
	}
}

func runtimeObservationMatches(
	target providers.RuntimeTarget,
	observation providers.RuntimeObservation,
	wantStates ...providers.RuntimeState,
) bool {
	return observation.MachineID == target.MachineID &&
		observation.ProviderResourceID == target.ProviderResourceID &&
		slices.Contains(wantStates, observation.State)
}
