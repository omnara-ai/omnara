package executionstore

import (
	"context"
	"sync"
	"time"
)

type StepDisposition uint8

const (
	StepRetained StepDisposition = iota
	StepFinished
	StepTransferred
)

type StepCompletion struct {
	mu       sync.Mutex
	pending  *stepCompletionResolution
	finished bool
	lost     func()
}

type stepCompletionResolution struct {
	stop func() bool
}

func (s *StepCompletion) Begin(ctx context.Context, canFinish bool) func(bool) {
	if s == nil || !canFinish {
		return func(bool) {}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	resolution := &stepCompletionResolution{}
	s.mu.Lock()
	s.pending = resolution
	resolution.stop = context.AfterFunc(ctx, func() { s.resolve(resolution, false) })
	s.mu.Unlock()
	return func(finished bool) {
		s.resolve(resolution, finished)
		resolution.stop()
		cancel()
	}
}

func (s *StepCompletion) resolve(resolution *stepCompletionResolution, finished bool) {
	s.mu.Lock()
	s.finished = s.finished || finished
	var lost func()
	if s.pending == resolution {
		s.pending = nil
		lost = s.lost
		s.lost = nil
	}
	if s.finished {
		lost = nil
	}
	s.mu.Unlock()
	if lost != nil {
		lost()
	}
}

func (s *StepCompletion) LeaseLost(cancel func()) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	if s.pending != nil {
		s.lost = cancel
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	cancel()
}
