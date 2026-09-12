package modal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestObserveRuntimeState(t *testing.T) {
	for _, test := range []struct {
		name             string
		present, running bool
		tagsErr, waitErr error
		want             providers.RuntimeState
		wantError        bool
	}{
		{name: "running", present: true, running: true, want: providers.RuntimeStateRunning},
		{name: "finished", present: true, want: providers.RuntimeStateTerminated},
		{name: "missing", want: providers.RuntimeStateTerminated},
		{
			name: "missing tags", present: true, running: true,
			tagsErr: status.Error(codes.NotFound, "missing"), want: providers.RuntimeStateTerminated,
		},
		{
			name: "missing poll", present: true, running: true,
			waitErr: status.Error(codes.NotFound, "missing"), want: providers.RuntimeStateTerminated,
		},
		{
			name: "tags failure", present: true, wantError: true,
			tagsErr: status.Error(codes.PermissionDenied, "denied"), want: providers.RuntimeStateUnknown,
		},
		{
			name: "poll failure", present: true, wantError: true,
			waitErr: status.Error(codes.Aborted, "offline"), want: providers.RuntimeStateUnknown,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rpc := newFakeControlPlane()
			rpc.tagsErr = test.tagsErr
			rpc.waitErr = test.waitErr
			target := runtimeTarget(t, fakeSandboxID("target"))
			if test.present {
				rpc.add(fakeSandboxID("target"), "owned", testOwnershipTags(t, target.MachineID), test.running)
			}
			observation, err := testProvider(t, rpc).ObserveRuntimeState(context.Background(), target)
			if (err != nil) != test.wantError {
				t.Fatalf("observe modal runtime: %v", err)
			}
			assertRuntimeObservation(t, observation, target, test.want)
		})
	}
}

func TestObserveRuntimeStateFailsOpenForForeignSandbox(t *testing.T) {
	rpc := newFakeControlPlane()
	target := runtimeTarget(t, fakeSandboxID("running"))
	rpc.add(fakeSandboxID("running"), "owned", map[string]string{machineTag: "another-machine"}, true)
	observation, err := testProvider(t, rpc).ObserveRuntimeState(context.Background(), target)
	if err != nil {
		t.Fatalf("observe modal runtime: %v", err)
	}
	assertRuntimeObservation(t, observation, target, providers.RuntimeStateUnknown)
}

func TestObserveRuntimeStatesRejectsDuplicateTargets(t *testing.T) {
	rpc := newFakeControlPlane()
	target := runtimeTarget(t, fakeSandboxID("running"))
	rpc.add(fakeSandboxID("running"), "owned", testOwnershipTags(t, target.MachineID), true)
	observations, err := testProvider(t, rpc).ObserveRuntimeStates(
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
	observed := 0
	rpc := newFakeControlPlane()
	rpc.tagsHook = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || deadline.Before(started.Add(5*time.Second)) || time.Until(deadline) > 5*time.Second {
			t.Errorf("unexpected observation deadline: %v, %v", deadline, ok)
		}
		observed++
		return status.Error(codes.NotFound, "missing")
	}
	_, err := testProvider(t, rpc).ObserveRuntimeStates(context.Background(), []providers.RuntimeTarget{
		runtimeTarget(t, fakeSandboxID("first")), runtimeTarget(t, fakeSandboxID("second")),
	})
	if err != nil || observed != 2 {
		t.Fatalf("observations: %d, %v", observed, err)
	}
}

func TestObserveRuntimeStatesTimeoutFailsOpen(t *testing.T) {
	for _, timeout := range []time.Duration{0, 250 * time.Millisecond} {
		t.Run(timeout.String(), func(t *testing.T) {
			ctx := context.Background()
			if timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, timeout)
				defer cancel()
			}
			rpc := newFakeControlPlane()
			rpc.tagsHook = func(callCtx context.Context) error {
				deadline, ok := callCtx.Deadline()
				if !ok {
					t.Error("observation has no deadline")
				}
				if _, bounded := ctx.Deadline(); bounded && time.Until(deadline) > time.Second {
					t.Error("observation extended the parent deadline")
				}
				<-callCtx.Done()
				return callCtx.Err()
			}
			observations, err := testProvider(t, rpc).ObserveRuntimeStates(ctx, []providers.RuntimeTarget{
				runtimeTarget(t, fakeSandboxID("stalled")),
			})
			if status.Code(err) != codes.DeadlineExceeded && !errors.Is(err, context.DeadlineExceeded) ||
				len(observations) != 0 {
				t.Fatalf("timed-out observations: %+v, %v", observations, err)
			}
		})
	}
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
