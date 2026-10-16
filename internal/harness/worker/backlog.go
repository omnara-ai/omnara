package worker

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/observability/metrics"
)

const (
	backlogSampleInterval = 15 * time.Second
	backlogSampleTimeout  = 5 * time.Second
)

// backlogSampler lets one claim loop at a time sample the global ready agent
// backlog once per interval, including while every slot is busy.
type backlogSampler struct {
	sample   func(context.Context) (executionstore.ReadyAgentBacklog, error)
	recorder *metrics.AgentBacklogRecorder
	log      *slog.Logger
	mu       sync.Mutex
	next     time.Time
}

func newBacklogSampler(
	sample func(context.Context) (executionstore.ReadyAgentBacklog, error),
	recorder *metrics.AgentBacklogRecorder,
	log *slog.Logger,
) *backlogSampler {
	if sample == nil || recorder == nil {
		return nil
	}
	return &backlogSampler{sample: sample, recorder: recorder, log: log}
}

// due fires when the next sample is due; it never fires when sampling is disabled.
func (s *backlogSampler) due() <-chan time.Time {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	next := s.next
	s.mu.Unlock()
	return time.After(max(time.Until(next), 0))
}

func (s *backlogSampler) sampleIfDue(ctx context.Context) {
	if s == nil || ctx.Err() != nil {
		return
	}
	s.mu.Lock()
	now := time.Now()
	if now.Before(s.next) {
		s.mu.Unlock()
		return
	}
	spread := backlogSampleInterval / 10
	s.next = now.Add(backlogSampleInterval - spread + time.Duration(rand.Int64N(int64(2*spread)+1)))
	s.mu.Unlock()
	backlog, err := s.sampleRecovering(ctx)
	s.recorder.Record(metrics.AgentBacklogSample{Ready: backlog.Ready, OldestReadyAge: backlog.OldestReadyAge}, err)
	if err != nil && ctx.Err() == nil {
		s.log.WarnContext(ctx, "sample ready agent backlog", "error", err)
	}
}

func (s *backlogSampler) sampleRecovering(ctx context.Context) (backlog executionstore.ReadyAgentBacklog, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = panicError("agent backlog sample", recovered)
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, backlogSampleTimeout)
	defer cancel()
	return s.sample(ctx)
}
