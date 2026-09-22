package executionstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
)

func TestMachineSourceForPoolValidatesAllSources(t *testing.T) {
	poolID := uuid.New()
	want := agentconfig.RuntimeMachine{MachinePoolID: poolID, MaxMachines: 2, Description: "pool"}
	contract := agentconfig.RuntimeContract{MachineSources: []agentconfig.RuntimeMachine{want}}
	source, found, err := machineSourceForPool(contract, poolID)
	if err != nil || !found || source.MachinePoolID != poolID ||
		source.MaxMachines != want.MaxMachines || source.Description != want.Description {
		t.Fatalf("pool source = %+v, found = %v, err = %v", source, found, err)
	}

	contract.MachineSources = append(contract.MachineSources, agentconfig.RuntimeMachine{})
	_, found, err = machineSourceForPool(contract, poolID)
	if err == nil || found || !strings.Contains(err.Error(), "machine_sources[1] has no machine source") {
		t.Fatalf("expected invalid later source to reject lookup, found = %v, err = %v", found, err)
	}
}
