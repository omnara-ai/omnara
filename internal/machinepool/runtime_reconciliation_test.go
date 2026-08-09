package machinepool

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestRuntimeReconciliationStateCooldownExpiresAndPrunes(t *testing.T) {
	state := newRuntimeReconciliationState()
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	err := providers.WithRetryAfter(
		errors.New("provider unavailable"),
		http.Header{"Retry-After": []string{"90"}},
	)

	state.recordFailure("scope-a", now, err)
	if !state.coolingDown("scope-a", now.Add(89*time.Second)) {
		t.Fatal("scope was not cooling down before Retry-After elapsed")
	}
	if state.coolingDown("scope-a", now.Add(90*time.Second)) {
		t.Fatal("scope cooldown did not expire")
	}
	state.pruneExpired(now.Add(90 * time.Second))
	state.mu.Lock()
	remaining := len(state.cooldowns)
	state.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("expired cooldown entries = %d, want 0", remaining)
	}
}

func TestRuntimeReconciliationStateBoundsProviderRetryAfter(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name       string
		retryAfter string
		want       time.Duration
	}{
		{name: "zero uses default", retryAfter: "0", want: providerRuntimeFailureCooldown},
		{name: "large is capped", retryAfter: "86400", want: providerRuntimeMaxFailureCooldown},
	} {
		t.Run(test.name, func(t *testing.T) {
			state := newRuntimeReconciliationState()
			err := providers.WithRetryAfter(
				errors.New("provider unavailable"),
				http.Header{"Retry-After": []string{test.retryAfter}},
			)
			state.recordFailure("scope", now, err)
			if !state.coolingDown("scope", now.Add(test.want-time.Nanosecond)) {
				t.Fatalf("scope stopped cooling before %s", test.want)
			}
			if state.coolingDown("scope", now.Add(test.want)) {
				t.Fatalf("scope remained cooling after %s", test.want)
			}
		})
	}
}

func TestValidRuntimeObservationsRejectsDuplicatesAndWrongResources(t *testing.T) {
	first := testRuntimeCandidate("first", "resource-a")
	second := testRuntimeCandidate("second", "resource-b")
	valid := validRuntimeObservations(
		[]executionstore.ProviderRuntimeCandidate{first, second},
		[]providers.RuntimeObservation{
			{MachineID: first.MachineID, ProviderResourceID: first.ProviderResourceID, State: providers.RuntimeStateRunning},
			{MachineID: first.MachineID, ProviderResourceID: first.ProviderResourceID, State: providers.RuntimeStateInactive},
			{MachineID: second.MachineID, ProviderResourceID: "wrong", State: providers.RuntimeStateRunning},
			{
				MachineID: second.MachineID, ProviderResourceID: second.ProviderResourceID,
				State: providers.RuntimeState("future"),
			},
		},
	)
	if len(valid) != 0 {
		t.Fatalf("valid observations = %+v, want none", valid)
	}
}

func testRuntimeCandidate(machineSeed, resourceID string) executionstore.ProviderRuntimeCandidate {
	return executionstore.ProviderRuntimeCandidate{
		MachineID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte(machineSeed)),
		ProviderResourceID: resourceID,
	}
}
