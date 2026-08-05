package tools

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// BackgroundRunner runs bounded work outside agent runtime ownership.
type BackgroundRunner interface {
	Submit(label string, task func(context.Context) error) bool
	TrySubmit(label string, task func(context.Context) error) bool
}

type backgroundTask struct {
	label string
	run   func(context.Context) error
}

type BackgroundExecutionRunner struct {
	log       *slog.Logger
	queue     chan backgroundTask
	stopTasks context.CancelFunc
	shutdown  chan struct{}
	stopOnce  sync.Once
	workers   sync.WaitGroup
}

func NewBackgroundExecutionRunner(
	ctx context.Context,
	log *slog.Logger,
	capacity int,
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
		stopTasks: stopTasks,
		shutdown:  make(chan struct{}),
	}
	runner.workers.Add(capacity)
	for range capacity {
		go runner.runWorker(taskCtx)
	}
	select {
	case <-ctx.Done():
		runner.beginShutdown()
	default:
		go func() {
			select {
			case <-ctx.Done():
				runner.beginShutdown()
			case <-runner.shutdown:
			}
		}()
	}
	return runner, nil
}

func (r *BackgroundExecutionRunner) Submit(
	label string,
	task func(context.Context) error,
) bool {
	if r == nil || task == nil {
		return false
	}
	select {
	case <-r.shutdown:
		return false
	default:
	}
	select {
	case <-r.shutdown:
		return false
	case r.queue <- backgroundTask{label: label, run: task}:
		select {
		case <-r.shutdown:
			return false
		default:
			return true
		}
	}
}

func (r *BackgroundExecutionRunner) TrySubmit(
	label string,
	task func(context.Context) error,
) bool {
	if r == nil || task == nil {
		return false
	}
	select {
	case <-r.shutdown:
		return false
	case r.queue <- backgroundTask{label: label, run: task}:
		select {
		case <-r.shutdown:
			return false
		default:
			return true
		}
	default:
		return false
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
			r.runTask(ctx, task)
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

func (r *BackgroundExecutionRunner) beginShutdown() {
	r.stopOnce.Do(func() {
		close(r.shutdown)
		r.stopTasks()
	})
}

func (r *BackgroundExecutionRunner) Shutdown() {
	if r == nil {
		return
	}
	r.beginShutdown()
	r.workers.Wait()
}
