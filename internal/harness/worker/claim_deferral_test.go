package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestClaimDeferralDoesNotBackOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		calls := 0
		store := &retainedRuntimeStore{claimErr: &executionstore.ClaimDeferredError{Cause: errors.New("invalid head")}}
		store.beforeClaimReturn = func() {
			calls++
			if calls == 4 {
				cancel()
			}
		}
		w := NewWorker(store, nil, Options{Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
		start := time.Now()
		w.claimLoop(ctx, make(chan struct{}, 1), &sync.WaitGroup{})
		if calls != 4 || time.Since(start) != 0 {
			t.Fatalf("claims=%d elapsed=%s", calls, time.Since(start))
		}
	})
}
