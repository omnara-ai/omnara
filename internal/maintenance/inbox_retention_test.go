package maintenance

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"
)

func TestInboxRetentionDrainsFullBatches(t *testing.T) {
	calls := 0
	count, exhausted, err := drainIntegrationInboxCleanup(t.Context(), func(context.Context) (int64, error) {
		calls++
		if calls <= 3 {
			return integrationInboxCleanupBatch, nil
		}
		return 2, nil
	})
	require.NoError(t, err)
	require.False(t, exhausted)
	require.EqualValues(t, 302, count)
	require.Equal(t, 4, calls)
}

func TestInboxRetentionBoundsSlowBatchAndPreservesCommittedCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		start := time.Now()
		count, exhausted, err := drainIntegrationInboxCleanup(t.Context(), func(ctx context.Context) (int64, error) {
			calls++
			if calls == 1 {
				return integrationInboxCleanupBatch, nil
			}
			<-ctx.Done()
			return 0, ctx.Err()
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.EqualValues(t, integrationInboxCleanupBatch, count)
		require.False(t, exhausted, "hard timeout is an error, not normal budget exhaustion")
		require.Equal(t, 2, calls)
		require.Equal(t, integrationInboxCleanupTimeout, time.Since(start))
		require.NoError(t, t.Context().Err(), "retention deadline must not cancel other maintenance")
	})
}

func TestInboxRetentionStopsOnErrorOrCancellation(t *testing.T) {
	failure := errors.New("database unavailable")
	calls := 0
	_, exhausted, err := drainIntegrationInboxCleanup(t.Context(), func(context.Context) (int64, error) {
		calls++
		return 0, failure
	})
	require.False(t, exhausted)
	require.ErrorIs(t, err, failure)
	require.Equal(t, 1, calls)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, exhausted, err = drainIntegrationInboxCleanup(ctx, func(context.Context) (int64, error) {
		t.Fatal("canceled pass must not start another batch")
		return 0, nil
	})
	require.False(t, exhausted)
	require.ErrorIs(t, err, context.Canceled)
}

func TestInboxRetentionSoftBudgetFinishesCurrentBatch(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		count, exhausted, err := drainIntegrationInboxCleanup(t.Context(), func(ctx context.Context) (int64, error) {
			calls++
			time.Sleep(2 * integrationInboxCleanupBudget) //nolint:omnaralint // Advance synctest's virtual clock.
			require.NoError(t, ctx.Err(), "soft budget must not cancel healthy SQL")
			return integrationInboxCleanupBatch, nil
		})
		require.NoError(t, err)
		require.True(t, exhausted)
		require.EqualValues(t, integrationInboxCleanupBatch, count)
		require.Equal(t, 1, calls, "do not start a batch after the soft budget")
	})
}
