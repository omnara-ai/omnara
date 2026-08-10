package providers

import (
	"context"
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type RuntimeProvider interface {
	MachineDeleter
	RuntimeStateObserver
}

type RuntimeProviderDefinition interface {
	NewRuntimeProvider(json.RawMessage, RuntimeConfig) (RuntimeProvider, error)
}

type RuntimeState string

const (
	RuntimeStateRunning      RuntimeState = "running"
	RuntimeStateInactive     RuntimeState = "inactive"
	RuntimeStateTransitional RuntimeState = "transitional"
	RuntimeStateTerminated   RuntimeState = "terminated"
	RuntimeStateUnknown      RuntimeState = "unknown"
)

func (state RuntimeState) Valid() bool {
	switch state {
	case RuntimeStateRunning,
		RuntimeStateInactive,
		RuntimeStateTransitional,
		RuntimeStateTerminated,
		RuntimeStateUnknown:
		return true
	default:
		return false
	}
}

type RuntimeTarget struct {
	InstallationID      storage.ID
	MachineID           storage.ID
	ProviderResourceID  string
	MachineProvisioning executionstore.MachineProvisioningConfig
}

func (target RuntimeTarget) UnknownObservation() RuntimeObservation {
	return RuntimeObservation{
		MachineID:          target.MachineID,
		ProviderResourceID: target.ProviderResourceID,
		State:              RuntimeStateUnknown,
	}
}

type RuntimeObservation struct {
	MachineID          storage.ID
	ProviderResourceID string
	State              RuntimeState
}

// RuntimeStateObserver supplies normalized provider-control-plane state. Bulk
// observations are discovery hints; callers must use the fresh single-target
// observation immediately before a destructive action.
type RuntimeStateObserver interface {
	ObserveRuntimeStates(context.Context, []RuntimeTarget) ([]RuntimeObservation, error)
	ObserveRuntimeState(context.Context, RuntimeTarget) (RuntimeObservation, error)
}
