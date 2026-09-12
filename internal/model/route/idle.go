package route

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

var errProviderIdleTimeout = fmt.Errorf("model provider response idle timeout: %w", context.DeadlineExceeded)

// Each watch covers a network wait, excluding local parsing and sink processing.
// The returned function must be called exactly once, after the wait completes.
func watchProviderIdle(cancel context.CancelCauseFunc, timeout time.Duration) func() {
	done := make(chan struct{})
	timer := time.AfterFunc(timeout, func() {
		cancel(errProviderIdleTimeout)
		close(done)
	})
	return func() {
		if !timer.Stop() {
			// Stop does not join an already started callback. Join it here so a
			// previous wait cannot cancel the request after the next wait begins.
			<-done
		}
	}
}

type idleResponseBody struct {
	io.ReadCloser
	ctx     context.Context //nolint:containedctx // The response body owns this request context until EOF or Close.
	cancel  context.CancelCauseFunc
	timeout time.Duration
}

func (b *idleResponseBody) Read(p []byte) (int, error) {
	stop := watchProviderIdle(b.cancel, b.timeout)
	n, err := b.ReadCloser.Read(p)
	stop()
	if errors.Is(context.Cause(b.ctx), errProviderIdleTimeout) {
		err = errProviderIdleTimeout
	}
	if err != nil {
		b.cancel(nil)
	}
	return n, err
}

func (b *idleResponseBody) Close() error {
	b.cancel(nil)
	return b.ReadCloser.Close()
}
