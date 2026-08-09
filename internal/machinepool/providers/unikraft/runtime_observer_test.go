package unikraft

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestUnikraftObserveRuntimeStatesGroupsByMetroAndChunks(t *testing.T) {
	api := &fakeAPI{instancesByUUID: map[string]instance{}}
	targets := make([]providers.RuntimeTarget, 0, runtimeObservationInitialBatchSize+3)
	for index := range runtimeObservationInitialBatchSize + 1 {
		target := runtimeTargetForTest(t, fmt.Sprintf("sfo-%03d", index), "sfo")
		targets = append(targets, target)
		api.instancesByUUID[target.ProviderResourceID] = ownedInstanceForTarget(
			t,
			target,
			"running",
		)
	}
	for index := range 2 {
		target := runtimeTargetForTest(t, fmt.Sprintf("fra-%03d", index), "fra")
		targets = append(targets, target)
		api.instancesByUUID[target.ProviderResourceID] = ownedInstanceForTarget(
			t,
			target,
			"running",
		)
	}

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	if len(api.batchGetRequests) != 3 {
		t.Fatalf("batch requests = %d, want 3", len(api.batchGetRequests))
	}
	for index, wantSize := range []int{runtimeObservationInitialBatchSize, 1, 2} {
		if got := len(api.batchGetRequests[index]); got != wantSize {
			t.Fatalf("batch request %d size = %d, want %d", index, got, wantSize)
		}
	}
	for index, observation := range observations {
		if observation.MachineID != targets[index].MachineID ||
			observation.ProviderResourceID != targets[index].ProviderResourceID ||
			observation.State != providers.RuntimeStateRunning {
			t.Fatalf("observation %d = %+v, want owned running target", index, observation)
		}
	}
}

func TestUnikraftObserveRuntimeStatesSplitsOversizedDetailedBatches(t *testing.T) {
	api := &fakeAPI{
		instancesByUUID: map[string]instance{},
		batchGetMaxSize: 2,
	}
	targets := make([]providers.RuntimeTarget, 7)
	for index := range targets {
		targets[index] = runtimeTargetForTest(t, fmt.Sprintf("large-%02d", index), "sfo")
		api.instancesByUUID[targets[index].ProviderResourceID] = ownedInstanceForTarget(
			t,
			targets[index],
			instanceStateRunning,
		)
	}

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe split runtime batches: %v", err)
	}
	if len(api.batchGetRequests) <= 1 || len(api.batchGetRequests[0]) != len(targets) {
		t.Fatalf("batch requests = %+v, want an oversized request followed by splits", api.batchGetRequests)
	}
	for index, observation := range observations {
		if observation.State != providers.RuntimeStateRunning {
			t.Fatalf("observation %d = %+v, want running", index, observation)
		}
	}
}

func TestUnikraftObserveRuntimeStatesFailsOpenOnAmbiguousResults(t *testing.T) {
	good := runtimeTargetForTest(t, "good", "sfo")
	missing := runtimeTargetForTest(t, "missing", "sfo")
	duplicate := runtimeTargetForTest(t, "duplicate", "sfo")
	foreign := runtimeTargetForTest(t, "foreign", "sfo")
	notFound := runtimeTargetForTest(t, "not-found", "sfo")
	itemError := runtimeTargetForTest(t, "item-error", "sfo")
	targets := []providers.RuntimeTarget{good, missing, duplicate, foreign, notFound, itemError}
	duplicateResult := ownedInstanceForTarget(t, duplicate, "running")
	api := &fakeAPI{
		batchGetResults: []instance{
			ownedInstanceForTarget(t, good, "running"),
			duplicateResult,
			duplicateResult,
			{
				Status: "success",
				UUID:   foreign.ProviderResourceID,
				Name:   "someone-elses-instance",
				State:  "running",
			},
			{Status: "error", Error: instanceNotFoundErrorCode, UUID: notFound.ProviderResourceID},
			{Status: "error", Error: 17, UUID: itemError.ProviderResourceID},
			{Status: "success", UUID: "unrequested", Name: "unrequested", State: "running"},
		},
		batchEnvelopeStatus: responseStatusPartialSuccess,
	}

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe runtime states: %v", err)
	}
	want := []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateTerminated,
		providers.RuntimeStateUnknown,
	}
	for index, wantState := range want {
		if got := observations[index].State; got != wantState {
			t.Fatalf("observation %d state = %q, want %q", index, got, wantState)
		}
	}
}

func TestUnikraftObserveRuntimeStatesUsesPerItemAuthority(t *testing.T) {
	running := runtimeTargetForTest(t, "running", "sfo")
	inactive := runtimeTargetForTest(t, "inactive", "sfo")
	missing := runtimeTargetForTest(t, "missing", "sfo")
	targets := []providers.RuntimeTarget{running, inactive, missing}
	results := []instance{
		ownedInstanceForTarget(t, running, instanceStateRunning),
		ownedInstanceForTarget(t, inactive, instanceStateStandby),
		{
			Status: responseStatusError,
			UUID:   missing.ProviderResourceID,
			Error:  instanceNotFoundErrorCode,
		},
	}

	t.Run("partial success preserves authoritative item results", func(t *testing.T) {
		api := &fakeAPI{
			batchGetResults:     results,
			batchEnvelopeStatus: responseStatusPartialSuccess,
		}
		observations, err := newTestProvider(api).ObserveRuntimeStates(
			context.Background(),
			targets,
		)
		if err != nil {
			t.Fatalf("observe partial-success batch: %v", err)
		}
		want := []providers.RuntimeState{
			providers.RuntimeStateRunning,
			providers.RuntimeStateInactive,
			providers.RuntimeStateTerminated,
		}
		for index, wantState := range want {
			if got := observations[index].State; got != wantState {
				t.Fatalf("observation %d state = %q, want %q", index, got, wantState)
			}
		}
	})

	t.Run("error envelope trusts only typed not found", func(t *testing.T) {
		api := &fakeAPI{
			batchGetResults:     results,
			batchEnvelopeStatus: responseStatusError,
		}
		observations, err := newTestProvider(api).ObserveRuntimeStates(
			context.Background(),
			targets,
		)
		if err != nil {
			t.Fatalf("observe error batch: %v", err)
		}
		want := []providers.RuntimeState{
			providers.RuntimeStateUnknown,
			providers.RuntimeStateUnknown,
			providers.RuntimeStateTerminated,
		}
		for index, wantState := range want {
			if got := observations[index].State; got != wantState {
				t.Fatalf("observation %d state = %q, want %q", index, got, wantState)
			}
		}
	})

	t.Run("success envelope makes typed not found ambiguous", func(t *testing.T) {
		api := &fakeAPI{batchGetResults: results}
		observations, err := newTestProvider(api).ObserveRuntimeStates(
			context.Background(),
			targets,
		)
		if err != nil {
			t.Fatalf("observe contradictory success batch: %v", err)
		}
		want := []providers.RuntimeState{
			providers.RuntimeStateRunning,
			providers.RuntimeStateInactive,
			providers.RuntimeStateUnknown,
		}
		for index, wantState := range want {
			if got := observations[index].State; got != wantState {
				t.Fatalf("observation %d state = %q, want %q", index, got, wantState)
			}
		}
	})

	t.Run("envelope errors make every item ambiguous", func(t *testing.T) {
		api := &fakeAPI{
			batchGetResults:        results,
			batchEnvelopeStatus:    responseStatusPartialSuccess,
			batchHasEnvelopeErrors: true,
		}
		observations, err := newTestProvider(api).ObserveRuntimeStates(
			context.Background(),
			targets,
		)
		if err != nil {
			t.Fatalf("observe batch with envelope errors: %v", err)
		}
		for index, observation := range observations {
			if observation.State != providers.RuntimeStateUnknown {
				t.Fatalf("observation %d state = %q, want unknown", index, observation.State)
			}
		}
	})
}

func TestUnikraftObserveRuntimeStatesRejectsDuplicateTargets(t *testing.T) {
	first := runtimeTargetForTest(t, "first", "sfo")
	duplicateResource := runtimeTargetForTest(t, "duplicate-resource", "sfo")
	duplicateResource.ProviderResourceID = first.ProviderResourceID
	duplicateMachine := runtimeTargetForTest(t, "duplicate-machine", "sfo")
	duplicateMachine.MachineID = first.MachineID
	targets := []providers.RuntimeTarget{first, duplicateResource, duplicateMachine}
	api := &fakeAPI{batchGetResults: []instance{
		{Status: "error", Error: instanceNotFoundErrorCode, UUID: first.ProviderResourceID},
	}}

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe duplicate targets: %v", err)
	}
	for index, observation := range observations {
		if observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation %d = %+v, want unknown", index, observation)
		}
	}
	if len(api.batchGetRequests) != 0 {
		t.Fatalf("duplicate targets reached provider: %v", api.batchGetRequests)
	}
}

func TestUnikraftRuntimeObservationDecodesOnlyMetroFromStoredProvisioning(t *testing.T) {
	target := runtimeTargetForTest(t, "stored-options", "sfo")
	target.MachineProvisioning.CPU = nil
	target.MachineProvisioning.MemoryMB = nil
	target.MachineProvisioning.ProviderOptions["image"] = json.RawMessage(`{"stored":"shape"}`)
	target.MachineProvisioning.ProviderOptions["startup_script"] = json.RawMessage(`["stored"]`)
	target.MachineProvisioning.ProviderOptions["removed_option"] = json.RawMessage(`true`)
	api := &fakeAPI{instancesByUUID: map[string]instance{
		target.ProviderResourceID: ownedInstanceForTarget(t, target, "running"),
	}}
	provider := newTestProvider(api)

	observations, err := provider.ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	)
	if err != nil {
		t.Fatalf("bulk observation with stored provisioning: %v", err)
	}
	if len(observations) != 1 || observations[0].State != providers.RuntimeStateRunning {
		t.Fatalf("bulk observations = %+v, want running", observations)
	}
	observation, err := provider.ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("fresh observation with stored provisioning: %v", err)
	}
	if observation.State != providers.RuntimeStateRunning {
		t.Fatalf("fresh observation = %+v, want running", observation)
	}
	if len(api.batchGetRequests) != 2 || len(api.getByUUIDRequests) != 0 {
		t.Fatalf(
			"provider calls batch=%v exact=%v, want two runtime batches",
			api.batchGetRequests,
			api.getByUUIDRequests,
		)
	}
}

func TestUnikraftRuntimeObservationRejectsInvalidMetroIdentity(t *testing.T) {
	target := runtimeTargetForTest(t, "invalid-metro", "sfo")
	target.MachineProvisioning.ProviderOptions["metro"] = json.RawMessage(`"bad/metro"`)
	api := &fakeAPI{instancesByUUID: map[string]instance{
		target.ProviderResourceID: ownedInstanceForTarget(t, target, "running"),
	}}
	provider := newTestProvider(api)

	observations, err := provider.ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	)
	if err != nil {
		t.Fatalf("bulk observation with invalid metro: %v", err)
	}
	if len(observations) != 1 || observations[0].State != providers.RuntimeStateUnknown {
		t.Fatalf("bulk observations = %+v, want unknown", observations)
	}
	observation, err := provider.ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("fresh observation with invalid metro: %v", err)
	}
	if observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("fresh observation = %+v, want unknown", observation)
	}
	if len(api.batchGetRequests) != 0 || len(api.getByUUIDRequests) != 0 {
		t.Fatalf(
			"invalid metro reached provider: batch=%v fresh=%v",
			api.batchGetRequests,
			api.getByUUIDRequests,
		)
	}
}

func TestUnikraftRuntimeStateMapping(t *testing.T) {
	tests := []struct {
		name   string
		result instance
		want   providers.RuntimeState
	}{
		{name: "running", result: instance{Status: "success", State: "running"}, want: providers.RuntimeStateRunning},
		{name: "standby", result: instance{Status: "success", State: "standby"}, want: providers.RuntimeStateInactive},
		{name: "stopped", result: instance{Status: "success", State: "stopped"}, want: providers.RuntimeStateInactive},
		{name: "starting", result: instance{Status: "success", State: "starting"}, want: providers.RuntimeStateTransitional},
		{name: "draining", result: instance{Status: "success", State: "draining"}, want: providers.RuntimeStateTransitional},
		{name: "stopping", result: instance{Status: "success", State: "stopping"}, want: providers.RuntimeStateTransitional},
		{name: "deleted", result: instance{Status: "success", State: "deleted"}, want: providers.RuntimeStateTerminated},
		{name: "template", result: instance{Status: "success", State: "template"}, want: providers.RuntimeStateUnknown},
		{
			name: "future state", result: instance{Status: "success", State: "hibernating"},
			want: providers.RuntimeStateUnknown,
		},
		{name: "missing item status", result: instance{State: "running"}, want: providers.RuntimeStateUnknown},
		{
			name: "non-canonical state", result: instance{Status: "success", State: "RUNNING"},
			want: providers.RuntimeStateUnknown,
		},
		{
			name:   "envelope status is not runtime state",
			result: instance{Status: "running", State: "standby"},
			want:   providers.RuntimeStateUnknown,
		},
		{
			name: "typed not found",
			result: instance{
				Status: responseStatusError,
				Error:  instanceNotFoundErrorCode,
				State:  instanceStateRunning,
			},
			want: providers.RuntimeStateTerminated,
		},
		{
			name: "not-found code without error status",
			result: instance{
				Status: responseStatusSuccess,
				Error:  instanceNotFoundErrorCode,
				State:  instanceStateStopped,
			},
			want: providers.RuntimeStateUnknown,
		},
		{
			name:   "other item error",
			result: instance{Status: "error", Error: 9, State: "stopped"},
			want:   providers.RuntimeStateUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeRuntimeState(tt.result); got != tt.want {
				t.Fatalf("normalized state = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnikraftObserveRuntimeStateUsesFreshUUIDReadAndValidatesOwnership(t *testing.T) {
	target := runtimeTargetForTest(t, "fresh", "sfo")
	running := ownedInstanceForTarget(t, target, "running")
	tests := []struct {
		name           string
		result         instance
		found          bool
		apiErr         error
		envelopeStatus responseStatus
		envelopeErrors bool
		provision      providers.RuntimeTarget
		want           providers.RuntimeState
		wantErr        bool
		wantCall       bool
	}{
		{
			name:     "owned running",
			result:   running,
			found:    true,
			want:     providers.RuntimeStateRunning,
			wantCall: true,
		},
		{
			name: "owned standby",
			result: instance{
				Status: "success",
				UUID:   running.UUID,
				Name:   running.Name,
				State:  "standby",
			},
			found:    true,
			want:     providers.RuntimeStateInactive,
			wantCall: true,
		},
		{
			name: "typed missing",
			result: instance{
				Status: "error",
				UUID:   running.UUID,
				Error:  instanceNotFoundErrorCode,
			},
			found:          true,
			envelopeStatus: responseStatusError,
			want:           providers.RuntimeStateTerminated,
			wantCall:       true,
		},
		{
			name: "success envelope makes typed missing ambiguous",
			result: instance{
				Status: "error",
				UUID:   running.UUID,
				Error:  instanceNotFoundErrorCode,
			},
			found:    true,
			want:     providers.RuntimeStateUnknown,
			wantCall: true,
		},
		{
			name:     "missing result is an ambiguous provider response",
			want:     providers.RuntimeStateUnknown,
			wantErr:  true,
			wantCall: true,
		},
		{
			name: "foreign allocation name",
			result: instance{
				Status: "success",
				UUID:   running.UUID,
				Name:   "foreign",
				State:  "running",
			},
			found:    true,
			want:     providers.RuntimeStateUnknown,
			wantCall: true,
		},
		{
			name: "mismatched uuid",
			result: instance{
				Status: "success",
				UUID:   "different",
				Name:   running.Name,
				State:  "running",
			},
			found:    true,
			want:     providers.RuntimeStateUnknown,
			wantCall: true,
		},
		{
			name:     "provider error",
			apiErr:   errors.New("provider unavailable"),
			want:     providers.RuntimeStateUnknown,
			wantErr:  true,
			wantCall: true,
		},
		{
			name:           "error envelope with successful-looking running item",
			result:         running,
			found:          true,
			envelopeStatus: responseStatusError,
			want:           providers.RuntimeStateUnknown,
			wantErr:        true,
			wantCall:       true,
		},
		{
			name: "error envelope with typed missing item",
			result: instance{
				Status: "error",
				UUID:   running.UUID,
				Error:  instanceNotFoundErrorCode,
			},
			found:          true,
			envelopeStatus: responseStatusError,
			want:           providers.RuntimeStateTerminated,
			wantCall:       true,
		},
		{
			name: "envelope errors make typed missing ambiguous",
			result: instance{
				Status: "error",
				UUID:   running.UUID,
				Error:  instanceNotFoundErrorCode,
			},
			found:          true,
			envelopeStatus: responseStatusError,
			envelopeErrors: true,
			want:           providers.RuntimeStateUnknown,
			wantCall:       true,
		},
		{
			name: "malformed provisioning",
			provision: providers.RuntimeTarget{
				InstallationID:     target.InstallationID,
				MachineID:          target.MachineID,
				ProviderResourceID: target.ProviderResourceID,
			},
			want: providers.RuntimeStateUnknown,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &fakeAPI{
				instancesByUUID:        map[string]instance{},
				batchGetErr:            tt.apiErr,
				batchEnvelopeStatus:    tt.envelopeStatus,
				batchHasEnvelopeErrors: tt.envelopeErrors,
			}
			if tt.found {
				api.instancesByUUID[target.ProviderResourceID] = tt.result
			}
			input := target
			if tt.provision.ProviderResourceID != "" {
				input = tt.provision
			}
			observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), input)
			if tt.wantErr != (err != nil) {
				t.Fatalf("error = %v, wantErr=%t", err, tt.wantErr)
			}
			if observation.State != tt.want {
				t.Fatalf("state = %q, want %q", observation.State, tt.want)
			}
			if tt.wantCall {
				if len(api.batchGetRequests) != 1 || len(api.batchGetRequests[0]) != 1 ||
					api.batchGetRequests[0][0] != target.ProviderResourceID {
					t.Fatalf("exact runtime requests = %v, want [[%s]]", api.batchGetRequests, target.ProviderResourceID)
				}
			} else if len(api.batchGetRequests) != 0 {
				t.Fatalf("exact runtime requests = %v, want none", api.batchGetRequests)
			}
		})
	}
}

func runtimeTargetForTest(t *testing.T, resourceID, metro string) providers.RuntimeTarget {
	t.Helper()
	return providers.RuntimeTarget{
		InstallationID:     testInstallationID(),
		MachineID:          uuid.New(),
		ProviderResourceID: resourceID,
		MachineProvisioning: testMachineProvisioning(t, map[string]any{
			"provider_options": map[string]any{"metro": metro},
		}),
	}
}

func ownedInstanceForTarget(
	t *testing.T,
	target providers.RuntimeTarget,
	state instanceState,
) instance {
	t.Helper()
	name, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err != nil {
		t.Fatalf("derive instance allocation name: %v", err)
	}
	return instance{
		Status: "success",
		UUID:   target.ProviderResourceID,
		Name:   name,
		State:  state,
	}
}
