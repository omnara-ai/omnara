package httpapi

import (
	"context"
	"testing"
	"time"

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
