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

func TestRuntimeReconciliationStatsCountNormalizedStates(t *testing.T) {
	var stats RuntimeReconciliationStats
	for _, state := range []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
		providers.RuntimeStateTransitional,
		providers.RuntimeStateTerminated,
		providers.RuntimeStateUnknown,
	} {
		stats.recordObservation(state)
	}
	if stats.Observed != 5 || stats.Running != 1 || stats.Inactive != 1 ||
		stats.Transitional != 1 || stats.Terminated != 1 || stats.Unknown != 1 {
		t.Fatalf("runtime observation stats = %+v", stats)
	}
}

func TestExactRuntimeObservationBoundary(t *testing.T) {
	for _, test := range []struct {
		name      string
		hasWake   bool
		expired   bool
		found     bool
		state     providers.RuntimeState
		wantExact bool
	}{
		{name: "bulk terminal requires confirmation", found: true, state: providers.RuntimeStateTerminated, wantExact: true},
		{name: "fresh terminal wake waits", hasWake: true, found: true, state: providers.RuntimeStateTerminated},
		{
			name: "expired terminal wake", hasWake: true, expired: true, found: true,
			state: providers.RuntimeStateTerminated, wantExact: true,
		},
		{name: "fresh unresolved wake waits", found: true, state: providers.RuntimeStateUnknown},
		{name: "expired unknown wake", expired: true, found: true, state: providers.RuntimeStateUnknown, wantExact: true},
		{
			name: "expired transitional wake", expired: true, found: true,
			state: providers.RuntimeStateTransitional, wantExact: true,
		},
		{name: "expired missing observation", expired: true, wantExact: true},
		{name: "expired running wake is decisive", expired: true, found: true, state: providers.RuntimeStateRunning},
		{name: "expired inactive wake is decisive", expired: true, found: true, state: providers.RuntimeStateInactive},
	} {
		t.Run(test.name, func(t *testing.T) {
			candidate := testRuntimeCandidate("exact-"+test.name, "resource")
			if test.hasWake {
				deadline := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
				candidate.WakeAttemptExpiresAt = &deadline
			}
			candidate.WakeAttemptExpired = test.expired
			observation := providers.RuntimeObservation{
				MachineID:          candidate.MachineID,
				ProviderResourceID: candidate.ProviderResourceID,
				State:              test.state,
			}
			if got := exactRuntimeObservationRequired(candidate, observation, test.found); got != test.wantExact {
				t.Fatalf("exact observation required = %t, want %t", got, test.wantExact)
			}
		})
	}
}

func TestExactRuntimeObservationTasksInterleaveScopes(t *testing.T) {
	tasks := interleaveExactRuntimeObservationTasks([][]runtimeExactObservationTask{
		exactRuntimeTasksForTest("first", 3),
		exactRuntimeTasksForTest("second", 2),
		exactRuntimeTasksForTest("third", 1),
	})
	want := []executionstore.ProviderRuntimeScopeKey{
		"first", "second", "third", "first", "second", "first",
	}
	if len(tasks) != len(want) {
		t.Fatalf("exact runtime tasks = %d, want %d", len(tasks), len(want))
	}
	for index, task := range tasks {
		if task.scopeKey != want[index] {
			t.Fatalf("exact runtime task %d scope = %q, want %q", index, task.scopeKey, want[index])
		}
	}
}

func TestRuntimeConfirmationStripeRotatesFairlyAcrossScopes(t *testing.T) {
	first := &runtimeConfirmationScope{
		scopeKey:   "first",
		candidates: runtimeCandidatesForTest("first", 3),
		start:      2,
	}
	second := &runtimeConfirmationScope{
		scopeKey:   "second",
		candidates: runtimeCandidatesForTest("second", 2),
	}
	tasks := buildRuntimeConfirmationStripe(
		[]*runtimeConfirmationScope{first, second},
		4,
	)
	want := []struct {
		scope          executionstore.ProviderRuntimeScopeKey
		candidateIndex int
	}{
		{scope: "first", candidateIndex: 2},
		{scope: "second", candidateIndex: 0},
		{scope: "first", candidateIndex: 0},
		{scope: "second", candidateIndex: 1},
	}
	if len(tasks) != len(want) {
		t.Fatalf("confirmation tasks = %d, want %d", len(tasks), len(want))
	}
	for index, task := range tasks {
		if task.scope.scopeKey != want[index].scope || task.candidateIndex != want[index].candidateIndex {
			t.Fatalf(
				"confirmation task %d = %s/%d, want %s/%d",
				index,
				task.scope.scopeKey,
				task.candidateIndex,
				want[index].scope,
				want[index].candidateIndex,
			)
		}
	}
	if first.next != 2 || second.next != 2 {
		t.Fatalf("confirmation progress = first:%d second:%d, want 2/2", first.next, second.next)
	}
	tasks = buildRuntimeConfirmationStripe([]*runtimeConfirmationScope{first, second}, 4)
	if len(tasks) != 1 || tasks[0].scope != first || tasks[0].candidateIndex != 1 || first.next != 3 {
		t.Fatalf("remaining confirmation tasks = %+v, first next=%d", tasks, first.next)
	}
}

func exactRuntimeTasksForTest(
	scopeKey executionstore.ProviderRuntimeScopeKey,
	count int,
) []runtimeExactObservationTask {
	tasks := make([]runtimeExactObservationTask, count)
	for index := range tasks {
		tasks[index].scopeKey = scopeKey
	}
	return tasks
}

func TestRuntimeConfirmationCursorResumesAfterLastAttempt(t *testing.T) {
	state := newRuntimeReconciliationState()
	candidates := runtimeCandidatesForTest("cursor", 3)
	scopeKey := executionstore.ProviderRuntimeScopeKey("scope")
	state.recordConfirmationCursor(scopeKey, candidates[1].MachineID)
	if start := state.confirmationStart(scopeKey, candidates); start != 2 {
		t.Fatalf("confirmation start = %d, want 2", start)
	}
	state.recordConfirmationCursor(scopeKey, candidates[2].MachineID)
	if start := state.confirmationStart(scopeKey, candidates); start != 0 {
		t.Fatalf("wrapped confirmation start = %d, want 0", start)
	}
	state.recordConfirmationCursor(scopeKey, uuid.New())
	if start := state.confirmationStart(scopeKey, candidates); start != 0 {
		t.Fatalf("missing-cursor confirmation start = %d, want 0", start)
	}
}

func runtimeCandidatesForTest(seed string, count int) []executionstore.ProviderRuntimeCandidate {
	candidates := make([]executionstore.ProviderRuntimeCandidate, count)
	for index := range count {
		candidates[index] = testRuntimeCandidate(
			seed+string(rune('a'+index)),
			seed+string(rune('a'+index)),
		)
	}
	return candidates
}

func testRuntimeCandidate(machineSeed, resourceID string) executionstore.ProviderRuntimeCandidate {
	return executionstore.ProviderRuntimeCandidate{
		MachineID:          uuid.NewSHA1(uuid.NameSpaceOID, []byte(machineSeed)),
		ProviderResourceID: resourceID,
	}
}
