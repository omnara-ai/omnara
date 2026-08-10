package daytona

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestDaytonaObserveRuntimeStatesPaginatesAndNormalizesMatches(t *testing.T) {
	running, runningSandbox := runtimeTargetAndSandbox(t, "running", "started")
	inactive, inactiveSandbox := runtimeTargetAndSandbox(t, "inactive", "paused")
	missing, _ := runtimeTargetAndSandbox(t, "missing", "started")
	duplicate, duplicateSandbox := runtimeTargetAndSandbox(t, "duplicate", "stopped")
	foreign, foreignSandbox := runtimeTargetAndSandbox(t, "foreign", "started")
	foreignSandbox.Labels = map[string]string{"omnara-machine": "someone-else"}
	malformed, malformedSandbox := runtimeTargetAndSandbox(t, "malformed", "started")
	malformedSandbox.Name = ""
	unknown, unknownSandbox := runtimeTargetAndSandbox(t, "unknown", "future_state")
	nextCursor := "page-2"

	api := newFakeAPI()
	api.listPages = []sandboxPage{
		{
			Items:      []sandbox{runningSandbox, duplicateSandbox, foreignSandbox},
			NextCursor: &nextCursor,
		},
		{
			Items: []sandbox{inactiveSandbox, duplicateSandbox, malformedSandbox, unknownSandbox},
		},
	}
	targets := []providers.RuntimeTarget{
		running,
		inactive,
		missing,
		duplicate,
		foreign,
		malformed,
		unknown,
	}
	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe daytona runtimes: %v", err)
	}
	wantStates := []providers.RuntimeState{
		providers.RuntimeStateRunning,
		providers.RuntimeStateInactive,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
		providers.RuntimeStateUnknown,
	}
	if len(observations) != len(targets) {
		t.Fatalf("observations = %d, want %d", len(observations), len(targets))
	}
	for index, observation := range observations {
		if observation.MachineID != targets[index].MachineID ||
			observation.ProviderResourceID != targets[index].ProviderResourceID ||
			observation.State != wantStates[index] {
			t.Fatalf("observation %d = %+v, want state %q for %+v", index, observation, wantStates[index], targets[index])
		}
	}
	if len(api.listQueries) != 2 {
		t.Fatalf("list calls = %d, want 2", len(api.listQueries))
	}
	if api.listQueries[0].Cursor != "" || api.listQueries[1].Cursor != nextCursor {
		t.Fatalf("list cursors = %q, %q", api.listQueries[0].Cursor, api.listQueries[1].Cursor)
	}
	for _, query := range api.listQueries {
		if query.Limit != 100 || !slices.Equal(query.States, daytonaObservableStates[:]) {
			t.Fatalf("list query = %+v", query)
		}
	}
}

func TestDaytonaObserveRuntimeStatesDoesNotListWithoutValidTargets(t *testing.T) {
	targets := []providers.RuntimeTarget{{}}
	api := newFakeAPI()
	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe invalid Daytona runtime target: %v", err)
	}
	if len(observations) != 1 || observations[0].State != providers.RuntimeStateUnknown {
		t.Fatalf("observations = %+v, want one unknown observation", observations)
	}
	if len(api.listQueries) != 0 {
		t.Fatalf("list queries = %+v, want none", api.listQueries)
	}
}

func TestDaytonaObserveRuntimeStateUsesFreshExactRead(t *testing.T) {
	t.Run("running and owned", func(t *testing.T) {
		target, current := runtimeTargetAndSandbox(t, "sandbox-1", "started")
		api := newFakeAPI()
		api.sandbox = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateRunning {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
		if len(api.getSandboxLookups) != 1 || api.getSandboxLookups[0] != target.ProviderResourceID {
			t.Fatalf("sandbox lookups = %#v", api.getSandboxLookups)
		}
	})

	t.Run("not found is terminated", func(t *testing.T) {
		target, _ := runtimeTargetAndSandbox(t, "missing", "started")
		api := newFakeAPI()
		api.missingSandboxIDs = map[string]bool{target.ProviderResourceID: true}
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateTerminated {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})

	t.Run("ownership mismatch is unknown", func(t *testing.T) {
		target, current := runtimeTargetAndSandbox(t, "foreign", "started")
		current.Name = "someone-else"
		current.Labels = map[string]string{"omnara-machine": "someone-else"}
		api := newFakeAPI()
		api.sandbox = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})

	t.Run("resource id mismatch is unknown", func(t *testing.T) {
		target, current := runtimeTargetAndSandbox(t, "expected", "started")
		current.ID = "different"
		api := newFakeAPI()
		api.sandbox = current
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err != nil || observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})

	t.Run("provider error fails open", func(t *testing.T) {
		target, _ := runtimeTargetAndSandbox(t, "errored", "started")
		api := newFakeAPI()
		api.getSandboxErr = apiError{StatusCode: http.StatusServiceUnavailable}
		observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), target)
		if err == nil || observation.State != providers.RuntimeStateUnknown {
			t.Fatalf("observation = %+v, error %v", observation, err)
		}
	})
}

func TestDaytonaObserveRuntimeStatesFailsOpenOnLaterPageError(t *testing.T) {
	target, _ := runtimeTargetAndSandbox(t, "sandbox-1", sandboxStateStarted)
	nextCursor := "page-2"
	api := newFakeAPI()
	api.listPages = []sandboxPage{{NextCursor: &nextCursor}}
	api.listErr = apiError{StatusCode: http.StatusBadRequest}
	api.listErrCall = 2

	_, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	)
	if err == nil || api.getSandboxCalls != 0 || len(api.listQueries) != 2 {
		t.Fatalf(
			"second-page error = %v, get calls = %d, list calls = %d",
			err,
			api.getSandboxCalls,
			len(api.listQueries),
		)
	}
}

func TestDaytonaObserveRuntimeStatesFailsOpenOnProviderError(t *testing.T) {
	target, _ := runtimeTargetAndSandbox(t, "sandbox-1", "started")
	api := newFakeAPI()
	api.listErr = apiError{StatusCode: http.StatusServiceUnavailable}
	_, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	)
	if err == nil || api.getSandboxCalls != 0 {
		t.Fatalf("observation error = %v, get calls = %d", err, api.getSandboxCalls)
	}
}

func TestDaytonaObserveRuntimeStatesRejectsRepeatedCursor(t *testing.T) {
	target, _ := runtimeTargetAndSandbox(t, "sandbox-1", "started")
	repeated := "same-cursor"
	api := newFakeAPI()
	api.listPages = []sandboxPage{
		{NextCursor: &repeated},
		{NextCursor: &repeated},
	}
	_, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target},
	)
	if err == nil || !strings.Contains(err.Error(), "repeated pagination cursor") {
		t.Fatalf("pagination error = %v", err)
	}
}

func TestDaytonaRuntimeStateAllowlist(t *testing.T) {
	tests := map[sandboxState]providers.RuntimeState{
		sandboxStateStarted:          providers.RuntimeStateRunning,
		" STARTED ":                  providers.RuntimeStateRunning,
		sandboxStateStopped:          providers.RuntimeStateInactive,
		sandboxStatePaused:           providers.RuntimeStateInactive,
		sandboxStateArchived:         providers.RuntimeStateInactive,
		sandboxStateDestroyed:        providers.RuntimeStateTerminated,
		sandboxStateCreating:         providers.RuntimeStateTransitional,
		sandboxStateRestoring:        providers.RuntimeStateTransitional,
		sandboxStateDestroying:       providers.RuntimeStateTransitional,
		sandboxStateStarting:         providers.RuntimeStateTransitional,
		sandboxStateStopping:         providers.RuntimeStateTransitional,
		sandboxStatePendingBuild:     providers.RuntimeStateTransitional,
		sandboxStateBuildingSnapshot: providers.RuntimeStateTransitional,
		sandboxStatePullingSnapshot:  providers.RuntimeStateTransitional,
		sandboxStateArchiving:        providers.RuntimeStateTransitional,
		sandboxStateResizing:         providers.RuntimeStateTransitional,
		sandboxStateSnapshotting:     providers.RuntimeStateTransitional,
		sandboxStateForking:          providers.RuntimeStateTransitional,
		sandboxStatePausing:          providers.RuntimeStateTransitional,
		sandboxStateResuming:         providers.RuntimeStateTransitional,
		sandboxStateError:            providers.RuntimeStateUnknown,
		sandboxStateBuildFailed:      providers.RuntimeStateUnknown,
		sandboxStateUnknown:          providers.RuntimeStateUnknown,
		"future_state":               providers.RuntimeStateUnknown,
		"":                           providers.RuntimeStateUnknown,
	}
	for input, want := range tests {
		t.Run(string(input), func(t *testing.T) {
			if got := daytonaRuntimeState(input); got != want {
				t.Fatalf("state %q = %q, want %q", input, got, want)
			}
		})
	}
}

func runtimeTargetAndSandbox(
	t *testing.T,
	resourceID string,
	state sandboxState,
) (providers.RuntimeTarget, sandbox) {
	t.Helper()
	target := providers.RuntimeTarget{
		InstallationID:     uuid.New(),
		MachineID:          uuid.New(),
		ProviderResourceID: resourceID,
	}
	expectedName, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err != nil {
		t.Fatal(err)
	}
	return target, sandbox{
		ID:     resourceID,
		Name:   expectedName,
		Labels: map[string]string{"omnara-machine": expectedName},
		State:  state,
	}
}
