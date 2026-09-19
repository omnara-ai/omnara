package executionstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

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

func TestDerivedLaunchRequiresProfileBase(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name                            string
		profile, base, subagent, reject bool
	}{
		{name: "missing profile base", profile: true, reject: true},
		{name: "pinned profile base", profile: true, base: true},
		{name: "unprofiled derived config"},
		{name: "subagent profile attribution", profile: true, subagent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			input := LaunchAgentInput{
				ProjectID:     uuid.New(),
				LaunchedBy:    identitystore.NewUserPrincipal(uuid.New()),
				DerivedConfig: &CreateAgentConfigInput{},
			}
			if test.profile {
				input.ProfileID = uuid.New()
			}
			if test.base {
				input.DerivedBaseConfigID = uuid.New()
			}
			if test.subagent {
				input.Subagent = &SubagentLaunch{}
			}
			_, err := validateLaunchAgentInput(input)
			if test.reject {
				require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
				require.ErrorContains(t, err, "requires a base config")
			} else {
				require.NoError(t, err)
			}
		})
	}
}
