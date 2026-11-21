package deliveryrunner

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type testRecorder struct {
	capacity, slots atomic.Int64
}

func (r *testRecorder) RecordCapacity(capacity int) { r.capacity.Store(int64(capacity)) }

func (r *testRecorder) StartDelivery() func() {
	r.slots.Add(1)
	return func() { r.slots.Add(-1) }
}

// queue claims from a buffered channel and reports an empty queue otherwise.
func queue(pending chan int, claims *atomic.Int64) func(context.Context) (int, error) {
	return func(context.Context) (int, error) {
		claims.Add(1)
		select {
		case delivery := <-pending:
			return delivery, nil
		default:
			return 0, storeerr.ErrNotFound
		}
	}
}

func TestRunCapsInFlightAndRefillsFreedSlots(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pending := make(chan int, 8)
		for delivery := range 5 {
			pending <- delivery
		}
		var claims, delivered atomic.Int64
		release := make(chan struct{})
		recorder := &testRecorder{}
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, Config[int]{
				MaxInFlight: 2, IdleInterval: time.Second, Claim: queue(pending, &claims), Recorder: recorder,
				Deliver: func(context.Context, int) {
					<-release
					delivered.Add(1)
				},
			})
		}()
		synctest.Wait()
		// Two deliveries hold both slots, and a full runner does not claim more.
		require.EqualValues(t, 2, recorder.capacity.Load())
		require.EqualValues(t, 2, recorder.slots.Load())
		require.EqualValues(t, 2, claims.Load())
		require.Len(t, pending, 3)
		time.Sleep(time.Minute) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		require.EqualValues(t, 2, claims.Load())

		// A finished delivery frees its slot for the next one at once.
		release <- struct{}{}
		synctest.Wait()
		require.EqualValues(t, 1, delivered.Load())
		require.EqualValues(t, 2, recorder.slots.Load())
		require.Len(t, pending, 2)

		close(release)
		synctest.Wait()
		require.EqualValues(t, 5, delivered.Load())
		require.EqualValues(t, 0, recorder.slots.Load())
		cancel()
		<-done
		require.EqualValues(t, 0, recorder.capacity.Load())
	})
}

func TestRunPollsAnEmptyQueueAtTheIdleInterval(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pending := make(chan int, 1)
		var claims, delivered atomic.Int64
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, Config[int]{
				MaxInFlight: 1, IdleInterval: time.Second, Claim: queue(pending, &claims),
				Deliver: func(context.Context, int) { delivered.Add(1) },
			})
		}()
		synctest.Wait()
		require.EqualValues(t, 1, claims.Load())
		time.Sleep(3 * time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		require.EqualValues(t, 4, claims.Load())

		pending <- 7
		time.Sleep(time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		require.EqualValues(t, 1, delivered.Load())
		cancel()
		<-done
	})
}

func TestRunReportsClaimFailuresButNotAnEmptyQueue(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		failure := errors.New("database unavailable")
		results := []error{storeerr.ErrNotFound, failure, storeerr.ErrNotFound}
		var claims atomic.Int64
		var reported []error
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, Config[int]{
				MaxInFlight: 1, IdleInterval: time.Second,
				Claim: func(context.Context) (int, error) {
					index := int(claims.Add(1)) - 1
					return 0, results[min(index, len(results)-1)]
				},
				Deliver:     func(context.Context, int) { t.Error("nothing was claimed") },
				ClaimFailed: func(_ context.Context, err error) { reported = append(reported, err) },
			})
		}()
		time.Sleep(2500 * time.Millisecond) //nolint:omnaralint // Advance synctest's virtual clock.
		synctest.Wait()
		cancel()
		<-done
		require.Equal(t, []error{failure}, reported)
	})
}

func TestRunWaitsForInFlightDeliveriesOnShutdown(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		pending := make(chan int, 1)
		pending <- 1
		var claims atomic.Int64
		var finished atomic.Bool
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() {
			defer close(done)
			Run(ctx, Config[int]{
				MaxInFlight: 1, IdleInterval: time.Second, Claim: queue(pending, &claims),
				Deliver: func(ctx context.Context, _ int) {
					<-ctx.Done()
					time.Sleep(time.Second) //nolint:omnaralint // Advance synctest's virtual clock.
					finished.Store(true)
				},
			})
		}()
		synctest.Wait()
		cancel()
		<-done
		require.True(t, finished.Load())
	})
}
