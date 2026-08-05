package tools

import (
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestBackgroundExecutionRunnerQueuesWorkAtCapacity(t *testing.T) {
	runner, err := NewBackgroundExecutionRunner(context.Background(), slog.Default(), 1)
	if err != nil {
		t.Fatalf("new background runner: %v", err)
	}
	defer runner.Shutdown()

	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	if !runner.Submit("first", func(context.Context) error {
		close(firstStarted)
		<-firstRelease
		return nil
	}) {
		t.Fatal("first background task was rejected")
	}
	<-firstStarted

	secondStarted := make(chan struct{})
	secondRelease := make(chan struct{})
	if !runner.Submit("second", func(context.Context) error {
		close(secondStarted)
		<-secondRelease
		return nil
	}) {
		t.Fatal("second background task was not queued")
	}

	optionalAccepted := make(chan bool, 1)
	go func() {
		optionalAccepted <- runner.TrySubmit(
			"optional",
			func(context.Context) error { return nil },
		)
	}()
	select {
	case accepted := <-optionalAccepted:
		if accepted {
			t.Fatal("optional background task was accepted while the queue was full")
		}
	case <-time.After(time.Second):
		t.Fatal("optional background submission blocked on queue capacity")
	}

	thirdStarted := make(chan struct{})
	thirdAccepted := make(chan bool, 1)
	go func() {
		thirdAccepted <- runner.Submit("third", func(context.Context) error {
			close(thirdStarted)
			return nil
		})
	}()
	select {
	case accepted := <-thirdAccepted:
		t.Fatalf("third submission returned while the bounded queue was full: accepted=%v", accepted)
	case <-time.After(25 * time.Millisecond):
	}

	close(firstRelease)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("queued second task did not start")
	}
	select {
	case accepted := <-thirdAccepted:
		if !accepted {
			t.Fatal("third background task was dropped")
		}
	case <-time.After(time.Second):
		t.Fatal("third submission did not proceed when queue space became available")
	}
	select {
	case <-thirdStarted:
		t.Fatal("runner exceeded its execution concurrency")
	default:
	}
	close(secondRelease)
	select {
	case <-thirdStarted:
	case <-time.After(time.Second):
		t.Fatal("queued third task did not start")
	}
}

func TestBackgroundExecutionRunnerShutdownCancelsWorkAndUnblocksSubmitters(t *testing.T) {
	runner, err := NewBackgroundExecutionRunner(context.Background(), slog.Default(), 1)
	if err != nil {
		t.Fatalf("new background runner: %v", err)
	}
	started := make(chan struct{})
	stopped := make(chan struct{})
	if !runner.Submit("running", func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}) {
		t.Fatal("running background task was rejected")
	}
	<-started
	queuedStarted := make(chan struct{})
	if !runner.Submit("queued", func(context.Context) error {
		close(queuedStarted)
		return nil
	}) {
		t.Fatal("background task was not queued")
	}
	blockedSubmission := make(chan bool, 1)
	go func() {
		blockedSubmission <- runner.Submit(
			"blocked",
			func(context.Context) error { return nil },
		)
	}()
	shutdownDone := make(chan struct{})
	go func() {
		runner.Shutdown()
		close(shutdownDone)
	}()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel in-flight background work")
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not wait for in-flight background work")
	}
	select {
	case accepted := <-blockedSubmission:
		if accepted {
			t.Fatal("blocked background task was accepted during shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not unblock a waiting submission")
	}
	select {
	case <-queuedStarted:
		t.Fatal("queued background work started during shutdown")
	default:
	}
	if runner.Submit("after-shutdown", func(context.Context) error { return nil }) {
		t.Fatal("background task was accepted after shutdown")
	}
}
