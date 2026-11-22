package revyl

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestObserveRuntimeState(t *testing.T) {
	id := testComputerID(t)
	mac := func(id uuid.UUID, state computerState) computer {
		return computer{ID: id, OS: computerOSMacOS, State: state}
	}
	for _, test := range []struct {
		name    string
		current computer
		found   bool
		want    providers.RuntimeState
	}{
		{"starting", mac(id, computerStateStarting), true, providers.RuntimeStateTransitional},
		{"active", mac(id, computerStateActive), true, providers.RuntimeStateRunning},
		{"closing", mac(id, computerStateClosing), true, providers.RuntimeStateTransitional},
		{"stopped", mac(id, computerStateStopped), true, providers.RuntimeStateTerminated},
		{"owner lost", mac(id, computerStateOwnerLost), true, providers.RuntimeStateTerminated},
		{"missing", computer{}, false, providers.RuntimeStateTerminated},
		{"foreign", mac(uuid.New(), computerStateActive), true, providers.RuntimeStateUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			api := &fakeAPI{getFound: test.found, getResults: []computer{test.current}}
			target := providers.RuntimeTarget{
				InstallationID:     testInstallationID(),
				MachineID:          testMachineID(),
				ProviderResourceID: id.String(),
			}

			observations, err := testProvider(api).ObserveRuntimeStates(context.Background(), []providers.RuntimeTarget{target})

			if err != nil || len(observations) != 1 || observations[0].State != test.want {
				t.Fatalf("ObserveRuntimeStates = %+v, %v; want %s", observations, err, test.want)
			}
		})
	}
}

func TestObserveRuntimeStateIgnoresUnownedResource(t *testing.T) {
	api := &fakeAPI{getErr: errors.New("must not be called")}
	target := providers.RuntimeTarget{
		InstallationID:     testInstallationID(),
		MachineID:          testMachineID(),
		ProviderResourceID: uuid.NewString(),
	}

	observation, err := testProvider(api).ObserveRuntimeState(context.Background(), target)

	if err != nil || observation.State != providers.RuntimeStateUnknown {
		t.Fatalf("ObserveRuntimeState = %+v, %v; want unknown", observation, err)
	}
}
