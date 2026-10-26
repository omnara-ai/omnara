package executionstore

import "testing"

func TestMergeTreeActivity(t *testing.T) {
	for _, test := range []struct {
		root, member, want string
	}{
		{SubagentStateIdle, SubagentStateRunning, SubagentStateRunning},
		{SubagentStateIdle, SubagentStateIdle, SubagentStateIdle},
		{SubagentStateRunning, SubagentStateWaitingOnInteraction, SubagentStateWaitingOnInteraction},
		{SubagentStateWaitingOnInteraction, SubagentStateRunning, SubagentStateWaitingOnInteraction},
		{SubagentStateArchived, SubagentStateRunning, SubagentStateArchived},
	} {
		got := mergeTreeActivity(AgentActivity{State: test.root}, AgentActivity{State: test.member}).State
		if got != test.want {
			t.Errorf("merge(%s, %s) = %s, want %s", test.root, test.member, got, test.want)
		}
	}
}
