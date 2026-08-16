package executionstore

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/resourcename"
)

func TestLaunchAgentNameUsesSafeDefaultForLegacyProfileName(t *testing.T) {
	profile := &AgentProfileRecord{ID: uuid.New(), Name: " legacy profile "}
	got := launchAgentName("", profile)
	profileID, err := publicid.Encode(publicid.KindAgentProfile, profile.ID)
	if err != nil {
		t.Fatalf("encode profile id: %v", err)
	}
	if got != "Agent from "+profileID {
		t.Fatalf("launch agent name = %q, want deterministic profile fallback", got)
	}
	if err := resourcename.Validate("agent name", got); err != nil {
		t.Fatalf("launch agent fallback name is invalid: %v", err)
	}
	if strings.Contains(got, profile.Name) {
		t.Fatalf("launch agent name %q copied invalid legacy profile name", got)
	}
}

func TestLaunchAgentNamePreservesValidProfileName(t *testing.T) {
	profile := &AgentProfileRecord{ID: uuid.New(), Name: "Research profile"}
	if got := launchAgentName("", profile); got != profile.Name {
		t.Fatalf("launch agent name = %q, want %q", got, profile.Name)
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
