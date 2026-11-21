// Package deliveryrunner runs the claim loop shared by outbound delivery
// senders: claim a queued delivery, hand it to the sender on its own
// goroutine, and keep no more than a fixed number in flight.
package deliveryrunner

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// Recorder reports how many delivery slots a sender has and how many are in
// use. Implementations must accept calls on a nil receiver.
type Recorder interface {
	RecordCapacity(capacity int)
	StartDelivery() (finished func())
}

// Config describes one sender's queue. Building requests and handling
// responses stay with the sender, inside Deliver.
type Config[D any] struct {
	// MaxInFlight is how many deliveries may be in progress at once.
	MaxInFlight int
	// IdleInterval is how long to wait before claiming again when the queue
	// had nothing due.
	IdleInterval time.Duration
	// Claim returns the next due delivery, or storeerr.ErrNotFound when there
	// is none. It bounds its own store call.
	Claim func(ctx context.Context) (D, error)
	// Deliver sends one claimed delivery and persists its outcome.
	Deliver func(ctx context.Context, delivery D)
	// ClaimFailed reports a claim error other than an empty queue.
	ClaimFailed func(ctx context.Context, err error)
	// Recorder is optional.
	Recorder Recorder
}

// Run claims and delivers until ctx is canceled, then waits for the
// deliveries in flight to return.
func Run[D any](ctx context.Context, config Config[D]) {
	if config.Recorder != nil {
		config.Recorder.RecordCapacity(config.MaxInFlight)
		defer config.Recorder.RecordCapacity(0)
	}
	inFlight := 0
	completed := make(chan struct{}, config.MaxInFlight)
	var deliveries sync.WaitGroup
	defer deliveries.Wait()
	for ctx.Err() == nil {
		if inFlight < config.MaxInFlight {
			delivery, err := config.Claim(ctx)
			if err == nil {
				inFlight++
				finished := func() {}
				if config.Recorder != nil {
					finished = config.Recorder.StartDelivery()
				}
				deliveries.Go(func() {
					config.Deliver(ctx, delivery)
					finished()
					completed <- struct{}{}
				})
				continue
			}
			if !errors.Is(err, storeerr.ErrNotFound) && ctx.Err() == nil && config.ClaimFailed != nil {
				config.ClaimFailed(ctx, err)
			}
		}
		// At capacity only a finished delivery frees a slot, so there is
		// nothing to poll for.
		var idle <-chan time.Time
		if inFlight < config.MaxInFlight {
			idle = time.After(config.IdleInterval)
		}
		select {
		case <-ctx.Done():
		case <-completed:
			inFlight--
		case <-idle:
		}
	}
}
