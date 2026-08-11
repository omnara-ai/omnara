package blaxel

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestBlaxelObserveRuntimeState(t *testing.T) {
	for _, test := range []struct {
		name       string
		state      string
		status     string
		want       providers.RuntimeState
		wrongOwner bool
	}{
		{name: "running", state: " running ", status: "deployed", want: providers.RuntimeStateRunning},
		{name: "standby", state: "STANDBY", status: "DEPLOYED", want: providers.RuntimeStateInactive},
		{
			name: "deleting takes precedence over running", state: "RUNNING",
			status: "DELETING", want: providers.RuntimeStateTerminated,
		},
		{
			name: "terminated takes precedence over standby", state: "STANDBY",
			status: "terminated", want: providers.RuntimeStateTerminated,
		},
		{
			name: "failed takes precedence over running", state: "RUNNING",
			status: "FAILED", want: providers.RuntimeStateTerminated,
		},
		{
			name: "deactivated takes precedence over standby", state: "STANDBY",
			status: "DEACTIVATED", want: providers.RuntimeStateTerminated,
		},
		{
			name: "deactivating takes precedence over running", state: "RUNNING",
			status: "deactivating", want: providers.RuntimeStateTransitional,
		},
		{name: "unknown runtime state", state: "PAUSED", status: "DEPLOYED", want: providers.RuntimeStateUnknown},
		{name: "missing runtime state", status: "DEPLOYED", want: providers.RuntimeStateUnknown},
		{
			name: "nonterminal deployment state without runtime state", status: "DEPLOYING",
			want: providers.RuntimeStateUnknown,
		},
		{
			name: "running runtime with transitional deployment", state: "RUNNING", status: "DEPLOYING",
			want: providers.RuntimeStateUnknown,
		},
		{
			name: "ownership mismatch", state: "RUNNING", status: "DEPLOYED",
			want: providers.RuntimeStateUnknown, wrongOwner: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			target := newRuntimeTarget(t)
			api := newRuntimeTestAPI()
			owned := ownedRuntimeSandbox(t, target, test.state, test.status)
			if test.wrongOwner {
				owned.Metadata.Labels[machineLabel] = uuid.NewString()
			}
			api.sandboxesByName[target.ProviderResourceID] = owned

			observation, err := newTestProvider(api).ObserveRuntimeState(
				context.Background(), target,
			)
			if err != nil {
				t.Fatalf("observe runtime state: %v", err)
			}
			assertRuntimeObservation(t, observation, target, test.want)
		})
	}
}

func TestBlaxelObserveRuntimeStateNotFoundIsTerminated(t *testing.T) {
	target := newRuntimeTarget(t)
	observation, err := newTestProvider(newRuntimeTestAPI()).ObserveRuntimeState(
		context.Background(), target,
	)
	if err != nil {
		t.Fatalf("observe missing runtime: %v", err)
	}
	assertRuntimeObservation(t, observation, target, providers.RuntimeStateTerminated)
}

func TestBlaxelObserveRuntimeStateFailsOpen(t *testing.T) {
	target := newRuntimeTarget(t)
	api := newRuntimeTestAPI()
	api.getErrors[target.ProviderResourceID] = errors.New("provider unavailable")
	if _, err := newTestProvider(api).ObserveRuntimeState(
		context.Background(), target,
	); err == nil || !errors.Is(err, api.getErrors[target.ProviderResourceID]) {
		t.Fatalf("observe provider error = %v", err)
	}

	invalid := target
	invalid.ProviderResourceID = "foreign-sandbox"
	observation, err := newTestProvider(api).ObserveRuntimeState(context.Background(), invalid)
	if err != nil {
		t.Fatalf("observe invalid target: %v", err)
	}
	assertRuntimeObservation(t, observation, invalid, providers.RuntimeStateUnknown)
	if len(api.getCalls) != 1 {
		t.Fatalf("get calls = %v, invalid target should not reach Blaxel", api.getCalls)
	}
}

func TestBlaxelObserveRuntimeStatesUsesTargetedGetsForSmallScope(t *testing.T) {
	targets := []providers.RuntimeTarget{newRuntimeTarget(t), newRuntimeTarget(t)}
	api := newRuntimeTestAPI()
	api.sandboxesByName[targets[0].ProviderResourceID] = ownedRuntimeSandbox(
		t, targets[0], "RUNNING", "DEPLOYED",
	)

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe small scope: %v", err)
	}
	if len(api.listCursors) != 0 || len(api.getCalls) != 2 {
		t.Fatalf("list cursors=%v get calls=%v", api.listCursors, api.getCalls)
	}
	assertRuntimeObservation(t, observations[0], targets[0], providers.RuntimeStateRunning)
	assertRuntimeObservation(t, observations[1], targets[1], providers.RuntimeStateTerminated)
}

func TestBlaxelObserveRuntimeStatesPaginatesAndIntersectsTargets(t *testing.T) {
	targets := make([]providers.RuntimeTarget, targetedRuntimeObservationLimit+3)
	for index := range targets {
		targets[index] = newRuntimeTarget(t)
	}
	api := newRuntimeTestAPI()
	api.listPages[""] = sandboxListPage{
		Sandboxes: []sandbox{
			ownedRuntimeSandbox(t, targets[0], "RUNNING", "DEPLOYED"),
			ownedRuntimeSandbox(t, targets[1], "STANDBY", "DEPLOYED"),
			ownedRuntimeSandbox(t, targets[2], "RUNNING", "DEPLOYED"),
			ownedRuntimeSandbox(t, targets[3], "", "DEPLOYED"),
			ownedRuntimeSandbox(t, targets[4], "RUNNING", "DEPLOYED"),
			{Metadata: resourceMetadata{Name: "unrelated"}, State: "RUNNING", Status: "DEPLOYED"},
		},
		HasMore: true, NextCursor: "next+/=",
	}
	api.listPages["next+/="] = sandboxListPage{Sandboxes: []sandbox{
		ownedRuntimeSandbox(t, targets[2], "STANDBY", "DEPLOYED"),
		ownedRuntimeSandbox(t, targets[5], "RUNNING", "DELETING"),
	}}
	api.listPages[""].Sandboxes[4].Metadata.Labels[installationLabel] = uuid.NewString()
	api.sandboxesByName[targets[3].ProviderResourceID] = ownedRuntimeSandbox(
		t, targets[3], "RUNNING", "DEPLOYED",
	)

	observations, err := newTestProvider(api).ObserveRuntimeStates(context.Background(), targets)
	if err != nil {
		t.Fatalf("observe bulk scope: %v", err)
	}
	if !slices.Equal(api.listCursors, []string{"", "next+/="}) ||
		!slices.Equal(api.listLimits, []int{runtimeObservationListPageSize, runtimeObservationListPageSize}) {
		t.Fatalf("list cursors=%v limits=%v", api.listCursors, api.listLimits)
	}
	if !slices.Equal(api.getCalls, []string{targets[3].ProviderResourceID}) {
		t.Fatalf("fallback GET calls = %v", api.getCalls)
	}
	wantStates := map[int]providers.RuntimeState{
		0: providers.RuntimeStateRunning,
		1: providers.RuntimeStateInactive,
		2: providers.RuntimeStateUnknown,
		3: providers.RuntimeStateRunning,
		4: providers.RuntimeStateUnknown,
		5: providers.RuntimeStateTerminated,
	}
	for index, target := range targets {
		want := providers.RuntimeStateUnknown
		if state, exists := wantStates[index]; exists {
			want = state
		}
		assertRuntimeObservation(t, observations[index], target, want)
	}
}

func TestBlaxelObserveRuntimeStatesFailsOpenOnMalformedPagination(t *testing.T) {
	for _, test := range []struct {
		name  string
		pages map[string]sandboxListPage
	}{
		{
			name: "missing next cursor",
			pages: map[string]sandboxListPage{
				"": {HasMore: true},
			},
		},
		{
			name: "repeated cursor",
			pages: map[string]sandboxListPage{
				"":       {HasMore: true, NextCursor: "repeat"},
				"repeat": {HasMore: true, NextCursor: "repeat"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			targets := make([]providers.RuntimeTarget, targetedRuntimeObservationLimit+1)
			for index := range targets {
				targets[index] = newRuntimeTarget(t)
			}
			api := newRuntimeTestAPI()
			api.listPages = test.pages
			if _, err := newTestProvider(api).ObserveRuntimeStates(
				context.Background(), targets,
			); err == nil {
				t.Fatal("malformed pagination unexpectedly succeeded")
			}
		})
	}
}

func TestBlaxelObserveRuntimeStatesReturnsProviderListError(t *testing.T) {
	targets := make([]providers.RuntimeTarget, targetedRuntimeObservationLimit+1)
	for index := range targets {
		targets[index] = newRuntimeTarget(t)
	}
	api := newRuntimeTestAPI()
	api.listError = errors.New("provider unavailable")
	if _, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(), targets,
	); err == nil || !errors.Is(err, api.listError) {
		t.Fatalf("bulk provider error = %v", err)
	}
}

func TestBlaxelObserveRuntimeStatesShrinksOversizedListPages(t *testing.T) {
	targets := make([]providers.RuntimeTarget, targetedRuntimeObservationLimit+1)
	for index := range targets {
		targets[index] = newRuntimeTarget(t)
	}
	api := newRuntimeTestAPI()
	api.maxListLimit = 25
	api.listPages[""] = sandboxListPage{Sandboxes: []sandbox{
		ownedRuntimeSandbox(t, targets[0], "RUNNING", "DEPLOYED"),
	}}

	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(),
		targets,
	)
	if err != nil {
		t.Fatalf("observe scope after oversized pages: %v", err)
	}
	if !slices.Equal(api.listLimits, []int{100, 50, 25}) {
		t.Fatalf("list limits = %v, want adaptive retries", api.listLimits)
	}
	assertRuntimeObservation(t, observations[0], targets[0], providers.RuntimeStateRunning)
}

func TestBlaxelObserveRuntimeStatesDuplicateRequestIsUnknown(t *testing.T) {
	target := newRuntimeTarget(t)
	api := newRuntimeTestAPI()
	api.sandboxesByName[target.ProviderResourceID] = ownedRuntimeSandbox(
		t, target, "RUNNING", "DEPLOYED",
	)
	observations, err := newTestProvider(api).ObserveRuntimeStates(
		context.Background(), []providers.RuntimeTarget{target, target},
	)
	if err != nil {
		t.Fatalf("observe duplicate targets: %v", err)
	}
	for _, observation := range observations {
		assertRuntimeObservation(t, observation, target, providers.RuntimeStateUnknown)
	}
	if len(api.getCalls) != 0 || len(api.listCursors) != 0 {
		t.Fatalf("duplicate target reached Blaxel: gets=%v lists=%v", api.getCalls, api.listCursors)
	}
}

type runtimeTestAPI struct {
	*fakeAPI
	listPages    map[string]sandboxListPage
	listError    error
	getErrors    map[string]error
	listCursors  []string
	listLimits   []int
	getCalls     []string
	maxListLimit int
}

func newRuntimeTestAPI() *runtimeTestAPI {
	return &runtimeTestAPI{
		fakeAPI:   newFakeAPI(),
		listPages: map[string]sandboxListPage{},
		getErrors: map[string]error{},
	}
}

func (f *runtimeTestAPI) ListSandboxes(
	_ context.Context,
	cursor string,
	limit int,
) (sandboxListPage, error) {
	f.listCursors = append(f.listCursors, cursor)
	f.listLimits = append(f.listLimits, limit)
	if f.maxListLimit > 0 && limit > f.maxListLimit {
		return sandboxListPage{}, providers.ErrResponseTooLarge
	}
	if f.listError != nil {
		return sandboxListPage{}, f.listError
	}
	page, exists := f.listPages[cursor]
	if !exists {
		return sandboxListPage{}, fmt.Errorf("unexpected cursor %q", cursor)
	}
	return page, nil
}

func (f *runtimeTestAPI) GetSandbox(
	_ context.Context,
	name string,
) (sandbox, bool, error) {
	f.getCalls = append(f.getCalls, name)
	if err := f.getErrors[name]; err != nil {
		return sandbox{}, false, err
	}
	target, found := f.sandboxesByName[name]
	return target, found, nil
}

func newRuntimeTarget(t *testing.T) providers.RuntimeTarget {
	t.Helper()
	target := providers.RuntimeTarget{
		InstallationID: testInstallationID(),
		MachineID:      uuid.New(),
	}
	name, err := providers.MachineAllocationName(target.InstallationID, target.MachineID)
	if err != nil {
		t.Fatalf("machine allocation name: %v", err)
	}
	target.ProviderResourceID = name
	return target
}

func ownedRuntimeSandbox(
	t testing.TB,
	target providers.RuntimeTarget,
	state, status string,
) sandbox {
	return sandbox{
		Metadata: resourceMetadata{
			Name:   target.ProviderResourceID,
			Labels: mustSandboxOwnershipLabels(t, target.InstallationID, target.MachineID),
		},
		State: sandboxRuntimeState(state), Status: sandboxDeploymentStatus(status),
	}
}

func assertRuntimeObservation(
	t *testing.T,
	observation providers.RuntimeObservation,
	target providers.RuntimeTarget,
	wantState providers.RuntimeState,
) {
	t.Helper()
	if observation.MachineID != target.MachineID ||
		observation.ProviderResourceID != target.ProviderResourceID ||
		observation.State != wantState {
		t.Fatalf("observation = %+v, want machine=%s resource=%q state=%q",
			observation, target.MachineID, target.ProviderResourceID, wantState)
	}
}
