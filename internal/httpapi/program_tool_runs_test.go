package httpapi

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestProgramToolRunsShareOneCapacity(t *testing.T) {
	t.Parallel()
	server := &Server{}
	WithProgramToolCapacity(2)(server)
	runs := &server.programToolRuns
	ctx := t.Context()
	require.True(t, runs.acquire(ctx, nil))
	require.True(t, runs.acquire(ctx, nil))

	waiting := make(chan bool, 1)
	go func() { waiting <- runs.acquire(ctx, nil) }()
	select {
	case <-waiting:
		t.Fatal("a third run started over the capacity")
	case <-time.After(50 * time.Millisecond):
	}
	runs.release()
	require.True(t, <-waiting, "a waiting run starts when one finishes")

	stopped, cancel := context.WithCancel(ctx)
	cancel()
	require.False(t, runs.acquire(stopped, nil), "a run stops waiting when the server shuts down")

	unbounded := &programToolRuns{}
	for range 100 {
		require.True(t, unbounded.acquire(ctx, nil))
	}
	unbounded.release()
}

func TestProgramToolRunsRunACallOnceAtATime(t *testing.T) {
	t.Parallel()
	runs := &programToolRuns{slots: make(chan struct{}, 1)}
	id := uuid.New()
	first, ctx, started := runs.begin(t.Context(), id)
	require.True(t, started)
	require.True(t, runs.runs(id))
	again, _, started := runs.begin(t.Context(), id)
	require.False(t, started, "a call already running here is not run again")
	require.Same(t, first, again)

	require.Equal(t, 1, runs.free(64))
	require.True(t, runs.tryAcquire())
	require.Zero(t, runs.free(64))
	require.False(t, runs.tryAcquire(), "the recovery poll never waits for capacity")
	runs.release()

	first.stop()
	require.Error(t, ctx.Err(), "stopping a run cancels it")
	runs.end(id, first)
	<-first.done
	require.False(t, runs.runs(id))
	next, _, started := runs.begin(t.Context(), id)
	require.True(t, started, "a call can run again once its last run ended")
	runs.end(id, next)

	runs.stop(time.Second)
	closed, _, started := runs.begin(t.Context(), uuid.New())
	require.Nil(t, closed, "a closing server starts no runs")
	require.False(t, started)
}
