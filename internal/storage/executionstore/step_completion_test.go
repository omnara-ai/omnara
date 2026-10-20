package executionstore

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestStepCompletionCoordinatesLeaseLoss(t *testing.T) {
	for _, finished := range []bool{false, true} {
		scope := &StepCompletion{}
		end := scope.Begin()
		canceled := false
		scope.LeaseLost(func() { canceled = true })
		require.False(t, canceled)
		end(finished)
		require.Equal(t, !finished, canceled)
		if finished {
			scope.LeaseLost(func() { t.Fatal("finished step canceled") })
		}
	}
	scope := &StepCompletion{}
	canceled := false
	scope.LeaseLost(func() { canceled = true })
	require.True(t, canceled)
}
