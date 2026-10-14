package worker

import (
	"context"
	"errors"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ContinuationOptions struct {
	MaxModelStarts int
	MaxDuration    time.Duration
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
