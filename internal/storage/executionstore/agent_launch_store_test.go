package executionstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
)

func TestLaunchAgentNamePreservesValidProfileName(t *testing.T) {
	profile := &AgentProfileRecord{ID: uuid.New(), Name: "Research profile"}
	got, err := launchAgentName(nil, profile)
	if err != nil {
		t.Fatalf("launchAgentName: %v", err)
	}
	if got != profile.Name {
		t.Fatalf("launch agent name = %q, want %q", got, profile.Name)
	}
}

func TestLaunchAgentNameRejectsInvalidProfileName(t *testing.T) {
	profile := &AgentProfileRecord{ID: uuid.New(), Name: " legacy profile "}
	if _, err := launchAgentName(nil, profile); err == nil || !strings.Contains(err.Error(), "whitespace") {
		t.Fatalf("launchAgentName error = %v, want whitespace rejection", err)
	}
}

func TestExpandLaunchMachineBindingRequestsUsesCompiledIDs(t *testing.T) {
	machineID := uuid.MustParse("019535d9-3df7-79fb-b466-fa907fa17f9e")
	machinePoolID := uuid.MustParse("019535d9-3df7-79fb-b466-fa907fa17f9f")
	requests, err := expandLaunchMachineBindingRequests([]launchMachineSource{
		{Index: 0, Contract: agentconfig.RuntimeMachine{MachineID: "mch_test"}, MachineID: machineID},
		{
			Index:         1,
			Contract:      agentconfig.RuntimeMachine{MachinePoolID: "mpo_test", InitialNumMachines: 2},
			MachinePoolID: machinePoolID,
		},
	})
	if err != nil {
		t.Fatalf("expand launch machine binding requests: %v", err)
	}
	if len(requests) != 3 {
		t.Fatalf("binding requests = %+v, want 3 requests", requests)
	}
	if requests[0].Source.MachineID != machineID || requests[0].PoolSlotIndex != 0 {
		t.Fatalf("unexpected machine request: %+v", requests[0])
	}
	if requests[1].Source.MachinePoolID != machinePoolID || requests[1].PoolSlotIndex != 0 ||
		requests[2].PoolSlotIndex != 1 {
		t.Fatalf("unexpected pool requests: %+v", requests)
	}
}
