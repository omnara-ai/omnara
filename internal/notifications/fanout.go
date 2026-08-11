package notifications

import (
	"context"
	"fmt"
	"sync"

	"github.com/omnara-ai/omnara/internal/redistore"
)

type agentFanout[T any] struct {
	bus       *RedisBus
	redisSub  *redistore.Subscription
	cancelSub context.CancelFunc

	ready   chan struct{}
	initErr error

	dispatchMu sync.RWMutex

	mu          sync.Mutex
	nextID      int64
	subscribers map[int64]func(context.Context, T)
	closed      bool
	removed     chan struct{}
	removeOnce  sync.Once

	decode func(context.Context, []byte) (T, bool)
	remove func(*agentFanout[T])
}

func newAgentFanout[T any](
	bus *RedisBus,
	decode func(context.Context, []byte) (T, bool),
	remove func(*agentFanout[T]),
) *agentFanout[T] {
	return &agentFanout[T]{
		bus:         bus,
		ready:       make(chan struct{}),
		removed:     make(chan struct{}),
		subscribers: map[int64]func(context.Context, T){},
		decode:      decode,
		remove:      remove,
	}
}

func (f *agentFanout[T]) subscribe(
	ctx context.Context,
	handler func(context.Context, T),
) (Subscription, bool) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return nil, false
	}
	f.nextID++
	id := f.nextID
	f.subscribers[id] = handler
	f.mu.Unlock()

	sub := &fanoutSubscription[T]{
		fanout: f,
		id:     id,
		done:   make(chan struct{}),
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = sub.Unsubscribe()
		case <-sub.done:
		}
	}()
	return sub, true
}

func (f *agentFanout[T]) waitRemoved(ctx context.Context) error {
	select {
	case <-f.removed:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *agentFanout[T]) markRemoved() {
	f.removeOnce.Do(func() {
		close(f.removed)
	})
}

func (f *agentFanout[T]) dispatch(ctx context.Context, payload []byte) {
	msg, ok := f.decode(ctx, payload)
	if !ok {
		return
	}

	f.dispatchMu.RLock()
	defer f.dispatchMu.RUnlock()

	f.mu.Lock()
	handlers := make([]func(context.Context, T), 0, len(f.subscribers))
	for _, handler := range f.subscribers {
		handlers = append(handlers, handler)
	}
	f.mu.Unlock()
	for _, handler := range handlers {
		f.invokeHandler(ctx, handler, msg)
	}
}

func (f *agentFanout[T]) invokeHandler(
	ctx context.Context,
	handler func(context.Context, T),
	msg T,
) {
	defer func() {
		if recovered := recover(); recovered != nil &&
			f.bus != nil && f.bus.log != nil {
			f.bus.log.Error(
				"notification subscriber panicked",
				"error", fmt.Sprint(recovered),
			)
		}
	}()
	handler(ctx, msg)
}

func (f *agentFanout[T]) unsubscribe(id int64) error {
	f.mu.Lock()
	delete(f.subscribers, id)
	if len(f.subscribers) > 0 || f.closed {
		f.mu.Unlock()
		return nil
	}
	f.closed = true
	f.mu.Unlock()

	f.bus.mu.Lock()
	if f.remove != nil {
		f.remove(f)
	}
	f.bus.mu.Unlock()

	var err error
	if f.redisSub != nil {
		err = f.redisSub.Unsubscribe()
	}
	if f.cancelSub != nil {
		f.cancelSub()
	}
	f.markRemoved()
	return err
}

type fanoutSubscription[T any] struct {
	fanout *agentFanout[T]
	id     int64
	once   sync.Once
	done   chan struct{}
	err    error
}

func (s *fanoutSubscription[T]) Unsubscribe() error {
	if s == nil {
		return nil
	}
	s.once.Do(func() {
		s.err = s.fanout.unsubscribe(s.id)
		s.fanout.dispatchMu.Lock()
		//nolint:staticcheck // SA2001: empty critical section is an intentional barrier for in-flight dispatch
		s.fanout.dispatchMu.Unlock()
		close(s.done)
	})
	return s.err
}

func (s *fanoutSubscription[T]) Done() <-chan struct{} {
	return s.done
}
