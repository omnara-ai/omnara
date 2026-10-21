package tools

import (
	"context"
	"errors"
	"sync"
)

type AsyncExecutionLimiter struct {
	slots chan struct{}
}

func NewAsyncExecutionLimiter(capacity int) *AsyncExecutionLimiter {
	return &AsyncExecutionLimiter{slots: make(chan struct{}, capacity)}
}

func (l *AsyncExecutionLimiter) acquire(ctx context.Context) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	select {
	case l.slots <- struct{}{}:
		var once sync.Once
		return func() {
			once.Do(func() { <-l.slots })
		}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

type AsyncExecutionScope struct {
	limiter *AsyncExecutionLimiter

	mu      sync.Mutex
	pending int
	started bool
	sealed  bool
	errs    []error
	done    chan struct{}
}

func NewAsyncExecutionScope(limiter *AsyncExecutionLimiter) *AsyncExecutionScope {
	return &AsyncExecutionScope{limiter: limiter, done: make(chan struct{})}
}

type asyncExecutionScopeContextKey struct{}

func WithAsyncExecutionScope(ctx context.Context, scope *AsyncExecutionScope) context.Context {
	return context.WithValue(ctx, asyncExecutionScopeContextKey{}, scope)
}

type AsyncExecutionReservation struct {
	scope     *AsyncExecutionScope
	release   func()
	startOnce sync.Once
	doneOnce  sync.Once
}

func ReserveAsyncExecution(ctx context.Context) (*AsyncExecutionReservation, error) {
	scope, _ := ctx.Value(asyncExecutionScopeContextKey{}).(*AsyncExecutionScope)
	if scope == nil {
		return &AsyncExecutionReservation{release: func() {}}, nil
	}
	release, err := scope.limiter.acquire(ctx)
	if err != nil {
		return nil, err
	}
	scope.mu.Lock()
	if scope.sealed {
		scope.mu.Unlock()
		release()
		return nil, errors.New("async execution scope is closed")
	}
	scope.pending++
	scope.mu.Unlock()
	return &AsyncExecutionReservation{scope: scope, release: release}, nil
}

func (r *AsyncExecutionReservation) Start() {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		if r.scope == nil {
			return
		}
		r.scope.mu.Lock()
		r.scope.started = true
		r.scope.mu.Unlock()
	})
}

func (r *AsyncExecutionReservation) Done(err error) {
	if r == nil {
		return
	}
	r.doneOnce.Do(func() {
		if r.release != nil {
			r.release()
		}
		if r.scope == nil {
			return
		}
		r.scope.mu.Lock()
		if err != nil {
			r.scope.errs = append(r.scope.errs, err)
		}
		r.scope.pending--
		if r.scope.sealed && r.scope.pending == 0 {
			close(r.scope.done)
		}
		r.scope.mu.Unlock()
	})
}

func (s *AsyncExecutionScope) Seal() {
	s.mu.Lock()
	if !s.sealed {
		s.sealed = true
		if s.pending == 0 {
			close(s.done)
		}
	}
	s.mu.Unlock()
}

func (s *AsyncExecutionScope) Started() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func (s *AsyncExecutionScope) Done() <-chan struct{} {
	return s.done
}

func (s *AsyncExecutionScope) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return errors.Join(s.errs...)
}
