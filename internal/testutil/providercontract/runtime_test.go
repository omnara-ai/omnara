package providercontract

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestWaitForPresentRuntimeObservationRetriesErrorsAndTransitions(t *testing.T) {
	target := providers.RuntimeTarget{
		MachineID:          uuid.New(),
		ProviderResourceID: "provider-resource",
	}
	calls := 0
	observation := WaitForPresentRuntimeObservation(
		t,
		context.Background(),
		target,
		func() (providers.RuntimeObservation, error) {
			calls++
			switch calls {
			case 1:
				return providers.RuntimeObservation{}, errors.New("provider unavailable")
			case 2:
				return providers.RuntimeObservation{
					MachineID:          target.MachineID,
					ProviderResourceID: target.ProviderResourceID,
					State:              providers.RuntimeStateTransitional,
				}, nil
			default:
				return providers.RuntimeObservation{
					MachineID:          target.MachineID,
					ProviderResourceID: target.ProviderResourceID,
					State:              providers.RuntimeStateRunning,
				}, nil
			}
		},
	)

	if calls != 3 || observation.State != providers.RuntimeStateRunning {
		t.Fatalf("wait result = %+v after %d calls", observation, calls)
	}
}
