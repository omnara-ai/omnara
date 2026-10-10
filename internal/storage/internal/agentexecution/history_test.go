package agentexecution_test

import (
	"fmt"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

type historyOutput struct {
	output agentexecution.ModelOutput
	tools  []historyTool
}

type historyTool struct {
	state          string
	kind           string
	resultSequence int64
	completedAt    time.Time
}

type executionHistory struct {
	view     agentexecution.ExecutionView
	contexts []agentexecution.ModelContext
	outputs  []historyOutput
}

func (h executionHistory) reconstruct() (agentexecution.ExecutionView, error) {
	view := h.view
	view.NormalContext, view.CompactionContext, view.ToolBatch = nil, nil, nil
	view.MaxNormalInputSequence, view.MaxContextInputSequence = 0, 0
	started := 0
	identities := make(map[string]bool)
	for _, context := range h.contexts {
		key := fmt.Sprintf("%s/%d/%d/%d", context.Operation, context.InputEventSequence,
			context.SourceEventSequenceEnd, context.Attempt)
		if identities[key] {
			return view, fmt.Errorf("%w: duplicate context identity", agentexecution.ErrInvalidState)
		}
		identities[key] = true
		if context.State == agentexecution.ContextStarted {
			started++
		}
		view.MaxContextInputSequence = max(view.MaxContextInputSequence, context.InputEventSequence)
		if context.Operation == agentexecution.OperationNormal {
			view.MaxNormalInputSequence = max(view.MaxNormalInputSequence, context.InputEventSequence)
			if context.TurnID == view.Turn.ID && (view.NormalContext == nil ||
				context.InputEventSequence > view.NormalContext.InputEventSequence ||
				(context.InputEventSequence == view.NormalContext.InputEventSequence &&
					context.Attempt > view.NormalContext.Attempt)) {
				view.NormalContext = &context
			}
		}
	}
	if started > 1 {
		return view, fmt.Errorf("%w: concurrent started contexts", agentexecution.ErrInvalidState)
	}
	for _, context := range h.contexts {
		if context.Operation != agentexecution.OperationCompaction || context.TurnID != view.Turn.ID {
			continue
		}
		if view.NormalContext == nil || context.InputEventSequence > view.NormalContext.InputEventSequence {
			return view, fmt.Errorf("%w: orphan compaction", agentexecution.ErrInvalidState)
		}
		if context.InputEventSequence != view.NormalContext.InputEventSequence {
			continue
		}
		if view.CompactionContext == nil ||
			context.SourceEventSequenceEnd < view.CompactionContext.SourceEventSequenceEnd ||
			(context.SourceEventSequenceEnd == view.CompactionContext.SourceEventSequenceEnd &&
				context.Attempt > view.CompactionContext.Attempt) {
			view.CompactionContext = &context
		}
	}
	for _, output := range h.outputs {
		if output.output.Context.TurnID != view.Turn.ID || output.output.Event.Sequence < view.StopSequence ||
			len(output.tools) == 0 {
			continue
		}
		batch := agentexecution.ToolBatch{Output: output.output}
		completion := agentexecution.BatchCompletion{}
		for _, tool := range output.tools {
			if tool.state != "completed" {
				batch.HasIncomplete = true
			}
			if tool.state == "awaiting_authorization" ||
				(tool.state == "ready" && (tool.kind == "built_in" || tool.kind == "mcp")) {
				batch.HasRunnable = true
			}
			if tool.state == "completed" {
				if tool.resultSequence <= output.output.Event.Sequence || tool.completedAt.IsZero() {
					return view, fmt.Errorf("%w: missing result authority", agentexecution.ErrInvalidState)
				}
				completion.LastResultSequence = max(completion.LastResultSequence, tool.resultSequence)
				if tool.completedAt.After(completion.ReadyAt) {
					completion.ReadyAt = tool.completedAt
				}
			}
		}
		if !batch.HasIncomplete {
			batch.Completion = &completion
			consumed := false
			for _, later := range h.outputs {
				if later.output.Context.TurnID == output.output.Context.TurnID &&
					later.output.Event.Sequence > completion.LastResultSequence &&
					later.output.Context.InputEventSequence >= completion.LastResultSequence {
					consumed = true
				}
			}
			if consumed {
				continue
			}
		}
		if view.ToolBatch != nil {
			return view, fmt.Errorf("%w: multiple unconsumed batches", agentexecution.ErrInvalidState)
		}
		view.ToolBatch = &batch
	}
	return view, nil
}

func TestSingleBatchHistoryTrace(t *testing.T) {
	t.Parallel()
	for _, seed := range []uint64{1, 7, 42, 101} {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			t.Parallel()
			rng := rand.New(rand.NewPCG(seed, seed+1))
			base := baseView()
			history := executionHistory{view: base, contexts: []agentexecution.ModelContext{*base.NormalContext}}
			previousResult := int64(0)
			for cycle := range 16 {
				context := history.contexts[len(history.contexts)-1]
				out := agentexecution.ModelOutput{ID: identity(byte(30 + cycle)), EventID: identity(byte(60 + cycle)),
					Context: context, Event: agentexecution.EventBoundary{
						Sequence: context.InputEventSequence + 1, Time: decisionTime().Add(-time.Second),
					}}
				history.outputs = append(history.outputs, historyOutput{output: out,
					tools: []historyTool{{state: "ready", kind: "built_in"}, {state: "ready", kind: "mcp"}}})
				view, err := history.reconstruct()
				require.NoError(t, err)
				require.Equal(t, out.ID, view.ToolBatch.Output.ID)
				selection, err := agentexecution.SelectNext(view, decisionTime())
				require.NoError(t, err)
				require.Equal(t, agentexecution.WorkTool, selection.Work)
				for step, index := range rng.Perm(2) {
					tool := &history.outputs[cycle].tools[index]
					tool.state, tool.resultSequence = "completed", out.Event.Sequence+int64(step)+1
					tool.completedAt = decisionTime().Add(-time.Duration(2-step) * time.Millisecond)
					view, err = history.reconstruct()
					require.NoError(t, err)
					selection, err = agentexecution.SelectNext(view, decisionTime())
					require.NoError(t, err)
					if step == 0 {
						require.Equal(t, agentexecution.WorkTool, selection.Work)
					} else {
						require.Equal(t, agentexecution.OriginTools, selection.Model.Origin)
					}
				}
				previousResult = view.ToolBatch.Completion.LastResultSequence
				next := context
				next.Attempt = 1
				next.ID, next.InputEventSequence, next.State = identity(byte(90+cycle)), previousResult,
					agentexecution.ContextStarted
				history.contexts = append(history.contexts, next)
				view, err = history.reconstruct()
				require.NoError(t, err)
				require.Equal(t, out.ID, view.ToolBatch.Output.ID)
				deadline := decisionTime().Add(time.Hour)
				next.State, next.Recovery, next.RetryAt = agentexecution.ContextFailed, agentexecution.RecoveryRetry, &deadline
				history.contexts[len(history.contexts)-1] = next
				view, err = history.reconstruct()
				require.NoError(t, err)
				selection, err = agentexecution.SelectNext(view, decisionTime())
				require.NoError(t, err)
				require.Equal(t, agentexecution.OriginRetry, selection.Model.Origin)
				require.Equal(t, agentexecution.WaitModelDeadline, selection.Wait)
				require.Equal(t, out.ID, view.ToolBatch.Output.ID)
				next.ID, next.Attempt, next.State = identity(byte(120+cycle)), 2, agentexecution.ContextSucceeded
				next.Recovery, next.RetryAt = agentexecution.RecoveryNone, nil
				history.contexts = append(history.contexts, next)
			}
			last := history.contexts[len(history.contexts)-1]
			history.outputs = append(history.outputs, historyOutput{output: agentexecution.ModelOutput{
				ID: identity(240), EventID: identity(241), Context: last,
				Event: agentexecution.EventBoundary{Sequence: previousResult + 1, Time: decisionTime()},
			}})
			view, err := history.reconstruct()
			require.NoError(t, err)
			require.Nil(t, view.ToolBatch)
		})
	}
}

func TestGoverningContextHistoryTrace(t *testing.T) {
	t.Parallel()
	view := compactionView()
	normal, compaction := *view.NormalContext, *view.CompactionContext
	compaction.State, compaction.Recovery = agentexecution.ContextFailed, agentexecution.RecoveryRetry
	deadline := decisionTime()
	compaction.RetryAt = &deadline
	history := executionHistory{view: view, contexts: []agentexecution.ModelContext{normal, compaction}}
	for attempt := int32(2); attempt <= 9; attempt++ {
		next := compaction
		next.ID, next.Attempt = identity(byte(10+attempt)), attempt
		history.contexts = append(history.contexts, next)
	}
	head, err := history.reconstruct()
	require.NoError(t, err)
	require.EqualValues(t, 9, head.CompactionContext.Attempt)
	selection, err := agentexecution.SelectNext(head, decisionTime())
	require.NoError(t, err)
	require.Equal(t, head.CompactionContext.ID, selection.Model.SourceContextID)
	for end := int64(8); end > 0; end-- {
		next := compaction
		next.ID, next.Attempt, next.SourceEventSequenceEnd = identity(byte(30+end)), 1, end
		history.contexts = append(history.contexts, next)
		head, err = history.reconstruct()
		require.NoError(t, err)
		require.Equal(t, next.ID, head.CompactionContext.ID)
	}
	terminal := &history.contexts[len(history.contexts)-1]
	terminal.State, terminal.Recovery, terminal.RetryAt = agentexecution.ContextSucceeded, agentexecution.RecoveryNone, nil
	head, err = history.reconstruct()
	require.NoError(t, err)
	selection, err = agentexecution.SelectNext(head, decisionTime())
	require.NoError(t, err)
	require.Nil(t, selection.Model)
	require.False(t, selection.TurnContinuable)
	next := normal
	next.ID, next.InputEventSequence, next.State = identity(80), 15, agentexecution.ContextStarted
	next.Recovery = agentexecution.RecoveryNone
	history.contexts = append(history.contexts, next)
	head, err = history.reconstruct()
	require.NoError(t, err)
	require.Equal(t, next.ID, head.NormalContext.ID)
	require.Nil(t, head.CompactionContext)
}

func TestHistoryRejectsMalformedObligations(t *testing.T) {
	t.Parallel()
	for _, malformed := range []string{"duplicate context", "two live contexts", "orphan compaction",
		"two incomplete batches", "two unconsumed complete batches", "missing result"} {
		t.Run(malformed, func(t *testing.T) {
			t.Parallel()
			view := baseView()
			history := executionHistory{view: view, contexts: []agentexecution.ModelContext{*view.NormalContext}}
			switch malformed {
			case "duplicate context":
				other := *view.NormalContext
				other.ID = identity(90)
				history.contexts = append(history.contexts, other)
			case "two live contexts":
				history.contexts[0].State = agentexecution.ContextStarted
				other := history.contexts[0]
				other.ID, other.InputEventSequence = identity(90), 11
				history.contexts = append(history.contexts, other)
			case "orphan compaction":
				other := *compactionView().CompactionContext
				other.InputEventSequence = 11
				history.contexts = append(history.contexts, other)
			case "two incomplete batches", "two unconsumed complete batches":
				for i := range 2 {
					o := historyOutput{output: output(view), tools: []historyTool{{state: "waiting", kind: "custom"}}}
					o.output.ID, o.output.EventID, o.output.Event.Sequence = identity(byte(90+i)), identity(byte(92+i)), 20+int64(i)
					if malformed == "two unconsumed complete batches" {
						o.tools[0] = historyTool{state: "completed", resultSequence: 30 + int64(i), completedAt: decisionTime()}
					}
					history.outputs = append(history.outputs, o)
				}
			case "missing result":
				history.outputs = []historyOutput{{output: output(view), tools: []historyTool{{state: "completed"}}}}
			}
			_, err := history.reconstruct()
			require.ErrorIs(t, err, agentexecution.ErrInvalidState)
		})
	}
}

func TestToolCustodyFacts(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"built_in", "mcp", "custom"} {
		for _, state := range []string{"awaiting_authorization", "ready", "running", "waiting", "completed"} {
			t.Run(kind+"/"+state, func(t *testing.T) {
				t.Parallel()
				view := baseView()
				history := executionHistory{view: view, contexts: []agentexecution.ModelContext{*view.NormalContext},
					outputs: []historyOutput{{output: output(view), tools: []historyTool{{
						state: state, kind: kind, resultSequence: 21, completedAt: decisionTime(),
					}}}}}
				view, err := history.reconstruct()
				require.NoError(t, err)
				got, err := agentexecution.SelectNext(view, decisionTime())
				require.NoError(t, err)
				runnable := state == "awaiting_authorization" || (state == "ready" && kind != "custom")
				require.Equal(t, runnable, got.Work == agentexecution.WorkTool)
				require.Equal(t, state != "completed", got.IncompleteTools)
				if !runnable && state != "completed" {
					require.Equal(t, agentexecution.WaitToolBatch, got.Wait)
				}
			})
		}
	}
}
