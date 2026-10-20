package executionstore

import (
	"context"
	"github.com/stretchr/testify/require"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestStepCompletionCoordinatesLeaseLoss(t *testing.T) {
	for _, finished := range []bool{false, true} {
		scope := &StepCompletion{}
		end := scope.Begin(t.Context(), true)
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

func TestStepCompletionCannotDeferLeaseLossWithoutFinishingClaim(t *testing.T) {
	scope := &StepCompletion{}
	end := scope.Begin(t.Context(), false)
	canceled := false
	scope.LeaseLost(func() { canceled = true })
	require.True(t, canceled)
	end(false)
}

func TestStepCompletionBoundsPendingResolution(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope := &StepCompletion{}
		end := scope.Begin(t.Context(), true)
		defer end(false)
		var canceled atomic.Bool
		scope.LeaseLost(func() { canceled.Store(true) })
		<-time.After(9 * time.Second)
		synctest.Wait()
		require.False(t, canceled.Load())
		<-time.After(time.Second)
		synctest.Wait()
		require.True(t, canceled.Load())
	})
}

func TestStepCompletionRespectsEarlierDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		scope := &StepCompletion{}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		end := scope.Begin(ctx, true)
		defer end(false)
		var canceled atomic.Bool
		scope.LeaseLost(func() { canceled.Store(true) })
		<-time.After(time.Second)
		synctest.Wait()
		require.True(t, canceled.Load())
	})
}
