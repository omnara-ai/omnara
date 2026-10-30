package machinedaemon

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
	"github.com/omnara-ai/omnara/internal/processaction"
	"github.com/stretchr/testify/require"
)

func TestOutputMatcherBatchesCompleteMatchingLines(t *testing.T) {
	t.Parallel()

	clock := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	matcher := newOutputMatcher("pact_test", regexp.MustCompile(`ERROR|listening`), 100, 3)
	matcher.now = func() time.Time { return clock }

	matcher.scan(outputWindow{Output: "ok\nERROR one\nlisten", Start: 100, Next: 119})
	require.Equal(t, []string{"ERROR one"}, matcher.pending)
	matcher.scan(outputWindow{Output: "ing on :3000\r\nfine\n", Start: 119, Next: 140})
	require.Equal(t, []string{"ERROR one", "listening on :3000"}, matcher.pending)
	require.Equal(t, int64(140), matcher.cursor)

	require.False(t, matcher.due(false), "matches wait to settle")
	clock = clock.Add(outputMatchSettle)
	require.True(t, matcher.due(false))
	seq, result := matcher.take()
	require.Equal(t, int64(4), seq)
	require.Equal(t, []string{"ERROR one", "listening on :3000"}, result.Lines)
	require.Equal(t, "ERROR|listening", result.Pattern)

	matcher.scan(outputWindow{Output: "ERROR two\n", Start: 140, Next: 150})
	clock = clock.Add(outputMatchSettle)
	require.False(t, matcher.due(false), "reports are spaced out")
	require.True(t, matcher.due(true), "a final flush does not wait")
	clock = clock.Add(outputMatchMinInterval)
	require.True(t, matcher.due(false))
	matcher.take()

	matcher.expired = true
	require.True(t, matcher.due(false), "an expired watch reports even without matches")
	_, result = matcher.take()
	require.True(t, result.Expired)
	require.Empty(t, result.Lines)
	require.False(t, matcher.expired)
}

func TestOutputMatcherLimitsLinesAndFinishesPartialLine(t *testing.T) {
	t.Parallel()

	matcher := newOutputMatcher("pact_test", regexp.MustCompile(`x`), 0, 0)
	var output strings.Builder
	for i := range outputMatchMaxLines + 3 {
		fmt.Fprintf(&output, "x%d\n", i)
	}
	output.WriteString("x" + strings.Repeat("y", outputMatchMaxLineRunes+5))
	matcher.scan(outputWindow{Output: output.String(), Next: int64(output.Len())})
	require.Len(t, matcher.pending, outputMatchMaxLines)
	require.Equal(t, 3, matcher.dropped)

	matcher.finish()
	require.Equal(t, 4, matcher.dropped, "the unterminated last line counts once the process ends")
	_, result := matcher.take()
	require.Equal(t, 4, result.Dropped)

	matcher.scan(outputWindow{Output: "\n", Start: 10, Next: 11})
	matcher.cursor = 0
	matcher.scan(outputWindow{Output: "x" + strings.Repeat("z", outputMatchMaxLineRunes+5) + "\n", Start: 5})
	require.Len(t, matcher.pending, 1)
	require.True(t, strings.HasSuffix(matcher.pending[0], "…"))
	require.Equal(t, outputMatchMaxLineRunes+1, len([]rune(matcher.pending[0])))
}

func TestOutputMatcherDropsPartialLineAfterRetentionGap(t *testing.T) {
	t.Parallel()

	matcher := newOutputMatcher("pact_test", regexp.MustCompile(`ERROR`), 0, 0)
	matcher.scan(outputWindow{Output: "ERROR cut", Next: 9})
	matcher.scan(outputWindow{Output: "off\nERROR whole\n", Start: 20, Next: 36})
	require.Equal(t, []string{"ERROR whole"}, matcher.pending)
}

func TestReplacingWatchDoesNotWaitForOldWatcherUnderApplyLocks(t *testing.T) {
	t.Parallel()

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	state := &runnerServerState{runCtx: runCtx}
	state.effectBoundaryMu.Lock()
	state.reconciliationMu.RLock()
	oldCtx, cancelOld := context.WithCancel(runCtx)
	old := &outputWatch{cancel: cancelOld, done: make(chan struct{})}
	var oldFlushed atomic.Bool
	go func() {
		defer close(old.done)
		<-oldCtx.Done()
		state.effectBoundaryMu.Lock()
		oldFlushed.Store(true)
		state.effectBoundaryMu.Unlock()
	}()
	state.watch = old

	started := make(chan error, 1)
	go func() {
		started <- state.startOutputWatch(&localProcessRunner{}, "pact_new", processaction.WatchPayload{
			Pattern: "ready", TimeoutSeconds: 60,
		})
	}()
	select {
	case err := <-started:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("replacing a watch waited for the old watcher while holding the apply locks")
	}
	replacement := state.watch
	require.NotSame(t, old, replacement)
	replacement.cancel()
	state.reconciliationMu.RUnlock()
	state.effectBoundaryMu.Unlock()
	<-old.done
	<-replacement.done
	require.True(t, oldFlushed.Load())
}

func TestReplacementWatchReportsExpiryWhenOldWatcherOutlastsIt(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	const (
		installationID       = "ins_replacement_watch_expiry"
		machineID            = "mch_replacement_watch_expiry"
		processID            = "prc_replacement_watch_expiry"
		supervisorInstanceID = "supervisor-instance-replacement-watch-expiry"
		supervisorToken      = "supervisor-token-replacement-watch-expiry"
	)
	root := t.TempDir()
	path := filepath.Join(root, "state.sqlite")
	store, err := statedb.Open(ctx, path, installationID, machineID)
	require.NoError(t, err)
	defer store.Close()
	require.NoError(t, store.ReserveProcess(ctx, statedb.Process{
		ProcessID:            processID,
		SupervisorInstanceID: supervisorInstanceID,
		SupervisorToken:      supervisorToken,
	}))
	require.NoError(t, store.MarkPrepared(ctx, processID, supervisorInstanceID))
	require.NoError(t, store.MarkAccepted(ctx, processID, supervisorInstanceID))
	supervisor, err := statedb.OpenSupervisor(
		ctx, path, installationID, machineID, processID, supervisorInstanceID, supervisorToken,
	)
	require.NoError(t, err)
	defer supervisor.Close()
	execute, err := supervisor.AuthorizeSpawnOnce(ctx)
	require.NoError(t, err)
	require.True(t, execute)
	require.NoError(t, supervisor.RecordSpawned(ctx, "process_group", "123"))

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	state := &runnerServerState{
		processState: supervisor,
		assignment:   ProcessAssignment{ID: processID},
		runCtx:       runCtx,
	}
	outputPath := filepath.Join(root, "output.buf")
	require.NoError(t, writeProcessOutputFile(outputPath, nil, 0))
	runner := &localProcessRunner{
		output: processOutput{
			path:         outputPath,
			limit:        1024,
			syncBytes:    1024,
			syncInterval: time.Hour,
		},
		terminalResultReady: make(chan struct{}),
		startedAt:           time.Now(),
	}
	_, err = runner.output.Write([]byte("starting\n"))
	require.NoError(t, err)
	defer runner.output.Close()

	oldCtx, cancelOld := context.WithCancel(runCtx)
	old := &outputWatch{cancel: cancelOld, done: make(chan struct{})}
	release := make(chan struct{})
	go func() {
		defer close(old.done)
		<-oldCtx.Done()
		<-release
	}()
	state.watch = old

	require.NoError(t, state.startOutputWatch(runner, "pact_replacement", processaction.WatchPayload{
		Pattern: "ready", TimeoutSeconds: 1,
	}))
	replacement := state.watch
	time.Sleep(time.Second + 200*time.Millisecond) //nolint:omnaralint // Outlast the watch's wall-clock deadline.
	close(release)
	select {
	case <-replacement.done:
	case <-ctx.Done():
		t.Fatal("the replacement watch did not finish")
	}

	reports, err := store.ReportsForProcess(ctx, processID)
	require.NoError(t, err)
	var expiries []daemonprotocol.OutputMatchResult
	for _, report := range reports {
		if report.Kind != statedb.ReportProcessOutputMatch {
			continue
		}
		var event daemonReportedEvent
		require.NoError(t, json.Unmarshal(report.Body, &event))
		var matched daemonprotocol.OutputMatchResult
		require.NoError(t, json.Unmarshal(event.Result, &matched))
		expiries = append(expiries, matched)
	}
	require.Equal(t, []daemonprotocol.OutputMatchResult{{
		WatchID: "pact_replacement", Pattern: "ready", Expired: true,
	}}, expiries)
}

func TestZeroTimeoutWatchCancelsCurrentWatch(t *testing.T) {
	t.Parallel()

	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	state := &runnerServerState{runCtx: runCtx}
	oldCtx, cancelOld := context.WithCancel(runCtx)
	old := &outputWatch{cancel: cancelOld, done: make(chan struct{})}
	go func() {
		defer close(old.done)
		<-oldCtx.Done()
	}()
	state.watch = old

	require.NoError(t, state.startOutputWatch(&localProcessRunner{}, "pact_cancel", processaction.WatchPayload{}))
	select {
	case <-old.done:
	case <-time.After(5 * time.Second):
		t.Fatal("a zero timeout watch did not cancel the current watch")
	}
	require.Same(t, old, state.watch)
}
