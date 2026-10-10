package worker

import (
	"context"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ContinuationOptions struct {
	// MaxModelStarts counts additional model work steps after the initial claim.
	// Zero disables continuation when these options are supplied explicitly.
	MaxModelStarts int
	MaxDuration    time.Duration
}

func continuationOptions(options *ContinuationOptions) ContinuationOptions {
	if options == nil {
		return ContinuationOptions{MaxModelStarts: 2, MaxDuration: 30 * time.Second}
	}
	result := *options
	if result.MaxDuration <= 0 {
		result.MaxDuration = 30 * time.Second
	}
	return result
}

func (options ContinuationOptions) canAdvance(elapsed time.Duration) bool {
	return options.MaxModelStarts > 0 && elapsed < options.MaxDuration
}

func runtimeExecutionError(err error, active *activeRuntime) error {
	if errors.Is(err, context.Canceled) && active.cancelRequested.Load() {
		return nil
	}
	if errors.Is(err, context.Canceled) && active.renewalFailed.Load() {
		return storeerr.ErrRuntimeLockInactive
	}
	return err
}

func (w *Worker) advanceOwnedWork(
	ctx context.Context,
	input executionstore.AdvanceOwnedAgentWorkInput,
) (claim executionstore.ClaimedAgentWork, continued bool, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("advance owned agent work", recovered)
		}
	}()
	return w.store.AdvanceOwnedAgentWork(ctx, input)
}
