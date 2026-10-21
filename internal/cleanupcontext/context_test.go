package cleanupcontext

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDetachSharesDrainCutoffAcrossNestedCleanup(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		drain, endDrain := context.WithCancel(t.Context())
		defer endDrain()
		turn, cancelTurn := context.WithCancel(WithDrain(t.Context(), drain))
		defer cancelTurn()
		first, cancelFirst := Detach(turn, 10*time.Second)
		defer cancelFirst()
		cancelTurn()
		require.NoError(t, first.Err())
		time.AfterFunc(time.Second, endDrain)
		<-time.After(500 * time.Millisecond)
		nested, cancelNested := Detach(first, 10*time.Second)
		defer cancelNested()
		synctest.Wait()
		require.NoError(t, nested.Err())
		<-time.After(500 * time.Millisecond)
		synctest.Wait()
		require.ErrorIs(t, first.Err(), context.Canceled)
		require.ErrorIs(t, nested.Err(), context.Canceled)
		late, cancelLate := Detach(nested, 10*time.Second)
		defer cancelLate()
		require.ErrorIs(t, late.Err(), context.Canceled)
	})
}

func TestDetachPreservesEarlierDrainDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		drain, endDrain := context.WithTimeout(t.Context(), time.Second)
		defer endDrain()
		cleanup, cancel := Detach(WithDrain(t.Context(), drain), 10*time.Second)
		defer cancel()
		cutoff, _ := drain.Deadline()
		deadline, ok := cleanup.Deadline()
		require.True(t, ok)
		require.Equal(t, cutoff, deadline)
		<-time.After(time.Second)
		synctest.Wait()
		require.Error(t, cleanup.Err())
	})
}
