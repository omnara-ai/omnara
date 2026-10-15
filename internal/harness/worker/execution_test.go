package worker

import (
	"context"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (w *Worker) RunOnce(ctx context.Context) (worked bool, err error) {
	now := time.Now().UTC()
	claim, handled, err := w.claimNext(ctx)
	if err != nil || !handled {
		return false, err
	}
	if claim.Kind == executionstore.AgentWorkNone {
		return true, nil
	}
	return true, w.executeClaimedWorkRecovering(ctx, claim, now)
}
