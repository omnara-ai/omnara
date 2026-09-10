package modal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestObserveRuntimeState(t *testing.T) {
	for _, test := range []struct {
		name    string
		current sandbox
		present bool
		want    providers.RuntimeState
	}{
		{
			name:    "running",
			current: sandbox{ID: "sb-running", Running: true},
			present: true,
			want:    providers.RuntimeStateRunning,
		},
		{name: "finished", current: sandbox{ID: "sb-finished"}, present: true, want: providers.RuntimeStateTerminated},
		{name: "missing", current: sandbox{ID: "sb-missing"}, want: providers.RuntimeStateTerminated},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := newFakeAPI()
			target := runtimeTarget(t, test.current.ID)
			if test.present {
				test.current.Tags = testOwnershipTags(t, target.MachineID)
				api.byID[test.current.ID] = test.current
			}
			observation, err := (&provider{api: api}).ObserveRuntimeState(context.Background(), target)
			if err != nil {
				t.Fatalf("observe modal runtime: %v", err)
			}
			assertRuntimeObservation(t, observation, target, test.want)
		})
	}
}

func TestObserveRuntimeStateFailsOpen(t *testing.T) {
	api := newFakeAPI()
	target := runtimeTarget(t, "sb-running")
	api.byID[target.ProviderResourceID] = sandbox{
		ID:      target.ProviderResourceID,
		Tags:    map[string]string{machineTag: "another-machine"},
		Running: true,
	}
	observation, err := (&provider{api: api}).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe modal runtime: %v", err)
	}
	assertRuntimeObservation(t, observation, target, providers.RuntimeStateUnknown)

	api.getByIDError = errors.New("provider unavailable")
	if _, err := (&provider{api: api}).ObserveRuntimeState(context.Background(), target); err == nil {
		t.Fatal("expected provider error")
	}
}

func TestObserveRuntimeStatesRejectsDuplicateTargets(t *testing.T) {
	api := newFakeAPI()
	target := runtimeTarget(t, "sb-running")
	api.byID[target.ProviderResourceID] = sandbox{
		ID:      target.ProviderResourceID,
		Tags:    testOwnershipTags(t, target.MachineID),
		Running: true,
	}
	observations, err := (&provider{api: api}).ObserveRuntimeStates(
		context.Background(),
		[]providers.RuntimeTarget{target, target},
	)
	if err != nil {
		t.Fatalf("observe modal runtimes: %v", err)
	}
	if len(observations) != 2 {
		t.Fatalf("observations = %d, want 2", len(observations))
	}
	for _, observation := range observations {
		assertRuntimeObservation(t, observation, target, providers.RuntimeStateUnknown)
	}
}

func TestObserveRuntimeStatesBoundsEachObservation(t *testing.T) {
	started := time.Now()
	var contexts []context.Context
	api := &runtimeObservationAPI{get: func(ctx context.Context, _ string) (sandbox, bool, error) {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(started.Add(5*time.Second)) || time.Until(deadline) > 5*time.Second {
			t.Fatalf("unexpected observation deadline: %v, %v", deadline, ok)
		}
		for _, previous := range contexts {
			if !errors.Is(previous.Err(), context.Canceled) {
				t.Fatal("previous observation context was not canceled")
			}
		}
		contexts = append(contexts, ctx)
		return sandbox{}, false, nil
	}}
	_, err := (&provider{api: api}).ObserveRuntimeStates(context.Background(), []providers.RuntimeTarget{
		runtimeTarget(t, "sb-first"), runtimeTarget(t, "sb-second"),
	})
	if err != nil || len(contexts) != 2 {
		t.Fatalf("observations: %d, %v", len(contexts), err)
	}
	if !errors.Is(contexts[1].Err(), context.Canceled) {
		t.Fatal("last observation context was not canceled")
	}
}

func TestObserveRuntimeStatesTimeoutFailsOpen(t *testing.T) {
	for _, timeout := range []time.Duration{0, 10 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			ctx := context.Background()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			api := &runtimeObservationAPI{get: func(callCtx context.Context, _ string) (sandbox, bool, error) {
				deadline, ok := callCtx.Deadline()
				if !ok {
					t.Fatal("observation has no deadline")
				}
				if parentDeadline, bounded := ctx.Deadline(); bounded && !deadline.Equal(parentDeadline) {
					t.Fatal("observation extended the parent deadline")
				}
				<-callCtx.Done()
				return sandbox{}, false, callCtx.Err()
			}}
			observations, err := (&provider{api: api}).ObserveRuntimeStates(ctx, []providers.RuntimeTarget{
				runtimeTarget(t, "sb-stalled"),
			})
			if !errors.Is(err, context.DeadlineExceeded) || len(observations) != 0 {
				t.Fatalf("timed-out observations: %+v, %v", observations, err)
			}
		})
	}
}

type runtimeObservationAPI struct {
	apiClient
	get func(context.Context, string) (sandbox, bool, error)
}

func (api *runtimeObservationAPI) GetSandboxByID(ctx context.Context, id string) (sandbox, bool, error) {
	return api.get(ctx, id)
}

func runtimeTarget(t *testing.T, providerResourceID string) providers.RuntimeTarget {
	t.Helper()
	return providers.RuntimeTarget{
		InstallationID:     testInstallationID(),
		MachineID:          uuid.New(),
		ProviderResourceID: providerResourceID,
	}
}

func assertRuntimeObservation(
	t *testing.T,
	observation providers.RuntimeObservation,
	target providers.RuntimeTarget,
	want providers.RuntimeState,
) {
	t.Helper()
	if observation.MachineID != target.MachineID ||
		observation.ProviderResourceID != target.ProviderResourceID ||
		observation.State != want {
		t.Fatalf(
			"observation = %+v, want machine %s resource %q state %q",
			observation,
			target.MachineID,
			target.ProviderResourceID,
			want,
		)
	}
}
