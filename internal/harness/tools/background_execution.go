package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/observability/metrics"
)

// BackgroundRunner runs bounded work outside agent runtime ownership.
type BackgroundRunner interface {
	Submit(label string, task func(context.Context) error) bool
	TrySubmit(label string, task func(context.Context) error) bool
}

type backgroundTask struct {
	label       string
	run         func(context.Context) error
	submittedAt time.Time
}

type BackgroundExecutionRunner struct {
	log        *slog.Logger
	queue      chan backgroundTask
	canceled   <-chan struct{}
	stopTasks  context.CancelFunc
	closing    chan struct{}
	idle       chan struct{}
	mu         sync.Mutex
	closed     bool
	unfinished int
	workers    sync.WaitGroup
	metrics    *metrics.BackgroundExecutionRecorder
}

func NewBackgroundExecutionRunner(
	ctx context.Context,
	log *slog.Logger,
	capacity int,
	metricSet *metrics.Set,
) (*BackgroundExecutionRunner, error) {
	if ctx == nil {
		return nil, errors.New("background execution context is required")
	}
	if capacity <= 0 {
		return nil, errors.New("background execution capacity must be positive")
	}
	if log == nil {
		log = slog.Default()
	}
	taskCtx, stopTasks := context.WithCancel(ctx)
	runner := &BackgroundExecutionRunner{
		log:       log,
		queue:     make(chan backgroundTask, capacity),
		canceled:  ctx.Done(),
		stopTasks: stopTasks,
		closing:   make(chan struct{}),
		idle:      make(chan struct{}),
	}
	if metricSet != nil {
		runner.metrics = metrics.NewBackgroundExecutionRecorder(metricSet, capacity, func() int { return len(runner.queue) })
	}
	runner.workers.Add(capacity)
	for range capacity {
		go runner.runWorker(taskCtx)
	}
	return runner, nil
}

func (r *BackgroundExecutionRunner) Submit(
	label string,
	task func(context.Context) error,
) bool {
	return r.submit(label, task, true)
}

func (r *BackgroundExecutionRunner) TrySubmit(
	label string,
	task func(context.Context) error,
) bool {
	return r.submit(label, task, false)
}

func (r *BackgroundExecutionRunner) submit(
	label string,
	task func(context.Context) error,
	waitForCapacity bool,
) bool {
	if r == nil || task == nil {
		return false
	}
	started := time.Now()
	r.metrics.StartSubmission()
	result := metrics.BackgroundSubmissionShutdown
	defer func() { r.metrics.RecordSubmission(result, time.Since(started)) }()
	if !r.reserve() {
		return false
	}
	queued := backgroundTask{label: label, run: task, submittedAt: started}
	if waitForCapacity {
		select {
		case r.queue <- queued:
			result = metrics.BackgroundSubmissionAccepted
			return true
		case <-r.closing:
		case <-r.canceled:
		}
	} else {
		select {
		case r.queue <- queued:
			result = metrics.BackgroundSubmissionAccepted
			return true
		default:
			result = metrics.BackgroundSubmissionFull
		}
	}
	r.finish()
	return false
}

func (r *BackgroundExecutionRunner) reserve() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	select {
	case <-r.canceled:
		return false
	default:
	}
	if r.closed {
		return false
	}
	r.unfinished++
	return true
}

func (r *BackgroundExecutionRunner) finish() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.unfinished--
	if r.closed && r.unfinished == 0 {
		close(r.idle)
	}
}

func (r *BackgroundExecutionRunner) stopAccepting() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return
	}
	r.closed = true
	close(r.closing)
	if r.unfinished == 0 {
		close(r.idle)
	}
}

func (r *BackgroundExecutionRunner) runWorker(ctx context.Context) {
	defer r.workers.Done()
	for {
		select {
		case <-ctx.Done():
			return
		case task := <-r.queue:
			if ctx.Err() != nil {
				return
			}
			r.metrics.RecordStart(time.Since(task.submittedAt))
			r.runTask(ctx, task)
			r.finish()
		}
	}
}

func (r *BackgroundExecutionRunner) runTask(ctx context.Context, task backgroundTask) {
	defer func() {
		if recovered := recover(); recovered != nil {
			r.log.Error(
				"background execution panicked",
				"task",
				task.label,
				"error",
				fmt.Sprint(recovered),
			)
		}
	}()
	if err := task.run(ctx); err != nil &&
		!(errors.Is(err, context.Canceled) && ctx.Err() != nil) {
		r.log.Error("background execution failed", "task", task.label, "error", err)
	}
}

func (r *BackgroundExecutionRunner) Shutdown() {
	if r == nil {
		return
	}
	r.stopAccepting()
	select {
	case <-r.idle:
	case <-r.canceled:
	}
	r.stopTasks()
	r.workers.Wait()
}
