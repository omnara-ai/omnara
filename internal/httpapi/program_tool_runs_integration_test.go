//go:build integration

package httpapi

import (
	"context"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestDaemonSocketProgramToolCallsShareOnePendingLimit(t *testing.T) {
	t.Parallel()
	const limit = 4
	f := newDaemonProgramToolCallFixture(t, func(*storage.Store) Option { return WithProgramToolPendingLimit(limit) })
	f.acceptProcess(t)
	ctx := t.Context()
	blocked, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocked.Rollback(context.WithoutCancel(ctx)) }()
	_, err = blocked.Exec(ctx, `LOCK TABLE processes IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)

	first, second := f.socket(), f.socket()
	offer := func(socket *daemonSocket, key string) daemonprotocol.ToolCallOffer {
		t.Helper()
		offer := programToolOffer(f.process.ProcessID, key, toolcatalog.ToolNameStorageList, `{}`)
		offer.OfferID = key
		require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
		return offer
	}
	admitted := map[*daemonSocket][]string{}
	for i := range limit {
		socket := first
		if i%2 == 1 {
			socket = second
		}
		key := "admitted-" + strconv.Itoa(i)
		offer(socket, key)
		admitted[socket] = append(admitted[socket], key)
	}
	require.Len(t, f.server.programToolRuns.pending, limit)
	for _, socket := range []*daemonSocket{first, second} {
		over := offer(socket, "over")
		refused := nextToolCallReport(t, socket)
		require.Equal(t, over.OfferID, refused.OfferID)
		require.Equal(t, daemonprotocol.AckStatusTransientError, refused.AckStatus)
		require.Equal(t, daemonprotocol.ErrorCodeToolCallLimitReached, refused.ErrorCode)
		socket.workMu.Lock()
		active := socket.toolCallsActive
		socket.workMu.Unlock()
		require.Less(t, active, daemonToolCallLimit, "each connection is far under its own limit")
	}

	require.NoError(t, blocked.Rollback(ctx))
	for socket, keys := range admitted {
		answered := map[string]bool{}
		for range keys {
			report := nextToolCallReport(t, socket)
			require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, report.State, "%+v", report)
			answered[report.OfferID] = true
		}
		require.Len(t, answered, len(keys))
		socket.toolCallsDone.Wait()
	}
	require.Empty(t, f.server.programToolRuns.pending, "a finished call frees its place")
	offer(first, "room")
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, nextToolCallReport(t, first).State)
	first.toolCallsDone.Wait()
}

type heldProgramToolExecutor struct {
	started  chan struct{}
	release  chan struct{}
	cleaned  atomic.Bool
	canceled atomic.Bool
}

func (e *heldProgramToolExecutor) RunProgramToolCall(
	ctx context.Context, _ executionstore.ProgramToolCallClaim, _ time.Duration,
) error {
	reservation, err := tools.ReserveAsyncExecution(ctx)
	if err != nil {
		return err
	}
	reservation.Start()
	go func() {
		defer reservation.Done(nil)
		close(e.started)
		<-ctx.Done()
		e.canceled.Store(true)
		<-e.release
		e.cleaned.Store(true)
	}()
	return nil
}

func TestServerCloseJoinsProgramToolRunsAndTheirCleanup(t *testing.T) {
	t.Parallel()
	executor := &heldProgramToolExecutor{started: make(chan struct{}), release: make(chan struct{})}
	f := newDaemonProgramToolCallFixture(t, func(*storage.Store) Option { return WithProgramToolExecutor(executor) })
	f.acceptProcess(t)
	socket := f.socket()
	socketCtx, disconnect := context.WithCancel(t.Context())
	defer func() {
		disconnect()
		socket.toolCallsDone.Wait()
	}()
	offer := programToolOffer(f.process.ProcessID, "held", toolcatalog.ToolNameStorageList, `{}`)
	require.NoError(t, socket.handleToolCallOffer(socketCtx, daemonprotocol.Message{ToolCallOffer: &offer}))
	select {
	case <-executor.started:
	case <-time.After(15 * time.Second):
		t.Fatal("the call never ran")
	}

	closed := make(chan struct{})
	go func() {
		f.server.Close()
		close(closed)
	}()
	require.Eventually(t, executor.canceled.Load, 5*time.Second, time.Millisecond, "closing cancels the run")
	select {
	case <-closed:
		t.Fatal("the server closed before its run's cleanup ended")
	case <-time.After(200 * time.Millisecond):
	}
	close(executor.release)
	select {
	case <-closed:
	case <-time.After(15 * time.Second):
		t.Fatal("the server did not close")
	}
	require.True(t, executor.cleaned.Load(), "the cleanup ended before the server closed")
	f.server.programToolRuns.mu.Lock()
	active := len(f.server.programToolRuns.active)
	f.server.programToolRuns.mu.Unlock()
	require.Zero(t, active)
	_, _, started := f.server.programToolRuns.begin(t.Context(), uuid.New())
	require.False(t, started, "a closed server starts no runs")
}
