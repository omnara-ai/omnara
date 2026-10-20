package executionstore

import "sync"

type StepDisposition uint8

const (
	StepRetained StepDisposition = iota
	StepFinished
	StepTransferred
)

type StepCompletion struct {
	mu       sync.Mutex
	pending  bool
	finished bool
	lost     func()
}

func (s *StepCompletion) Begin() func(bool) {
	if s == nil {
		return func(bool) {}
	}
	s.mu.Lock()
	s.pending = true
	s.mu.Unlock()
	return func(finished bool) {
		s.mu.Lock()
		s.pending = false
		s.finished = s.finished || finished
		lost := s.lost
		s.lost = nil
		if s.finished {
			lost = nil
		}
		s.mu.Unlock()
		if lost != nil {
			lost()
		}
	}
}

func (s *StepCompletion) LeaseLost(cancel func()) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	if s.pending {
		s.lost = cancel
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()
	cancel()
}
