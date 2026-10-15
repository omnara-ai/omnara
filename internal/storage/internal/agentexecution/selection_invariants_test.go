package agentexecution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestSelectNextRejectsMalformedViews(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*agentexecution.ExecutionView)
	}{
		{"missing agent", func(v *agentexecution.ExecutionView) { v.AgentID = uuid.Nil }},
		{"unknown lifecycle", func(v *agentexecution.ExecutionView) { v.State = "unknown" }},
		{"negative stop", func(v *agentexecution.ExecutionView) { v.StopSequence = -1 }},
		{"normal beyond all contexts", func(v *agentexecution.ExecutionView) { v.MaxNormalInputSequence++ }},
		{"missing turn", func(v *agentexecution.ExecutionView) { v.Turn = nil }},
		{"missing turn id", func(v *agentexecution.ExecutionView) { v.Turn.ID = uuid.Nil }},
		{"reversed openings", func(v *agentexecution.ExecutionView) { v.Turn.LastOpeningSequence-- }},
		{"wrong agent context", func(v *agentexecution.ExecutionView) { v.NormalContext.AgentID = identity(99) }},
		{"wrong turn context", func(v *agentexecution.ExecutionView) { v.NormalContext.TurnID = identity(99) }},
		{"context before turn", func(v *agentexecution.ExecutionView) { v.NormalContext.InputEventSequence-- }},
		{"bad attempt", func(v *agentexecution.ExecutionView) { v.NormalContext.Attempt = 0 }},
		{"unknown context state", func(v *agentexecution.ExecutionView) { v.NormalContext.State = "unknown" }},
		{"unknown recovery", func(v *agentexecution.ExecutionView) { v.NormalContext.Recovery = "unknown" }},
		{"recovery on success", func(v *agentexecution.ExecutionView) {
			v.NormalContext.Recovery = agentexecution.RecoveryCompact
		}},
		{"retry without deadline", func(v *agentexecution.ExecutionView) {
			v.NormalContext.State, v.NormalContext.Recovery = agentexecution.ContextFailed, agentexecution.RecoveryRetry
		}},
		{"deadline without retry", func(v *agentexecution.ExecutionView) {
			deadline := decisionTime()
			v.NormalContext.RetryAt = &deadline
		}},
		{"normal with range", func(v *agentexecution.ExecutionView) { v.NormalContext.SourceEventSequenceEnd = 1 }},
		{"duplicate opening", func(v *agentexecution.ExecutionView) {
			v.NormalContext.Opening.InputIDs = []uuid.UUID{identity(3), identity(3)}
		}},
		{"nil opening identity", func(v *agentexecution.ExecutionView) {
			v.NormalContext.Opening.InputIDs = []uuid.UUID{uuid.Nil}
		}},
		{"opening past watermark", func(v *agentexecution.ExecutionView) { v.NormalContext.Opening.EventSequence++ }},
		{"missing opening boundary", func(v *agentexecution.ExecutionView) { v.NormalContext.Opening.EventSequence = 0 }},
		{"orphan compaction", func(v *agentexecution.ExecutionView) {
			*v = compactionView()
			v.NormalContext = nil
		}},
		{"compaction wrong watermark", func(v *agentexecution.ExecutionView) {
			*v = compactionView()
			v.CompactionContext.InputEventSequence++
			v.MaxContextInputSequence++
		}},
		{"wrong config scope", func(v *agentexecution.ExecutionView) {
			v.Config = boundary(*v, 8, 20)
			v.Config.AgentID = identity(99)
		}},
		{"runnable completed batch", func(v *agentexecution.ExecutionView) {
			*v = completedBatchView()
			v.ToolBatch.HasRunnable = true
		}},
		{"incomplete with completion", func(v *agentexecution.ExecutionView) {
			*v = completedBatchView()
			v.ToolBatch.HasIncomplete = true
		}},
		{"complete without frontier", func(v *agentexecution.ExecutionView) {
			*v = completedBatchView()
			v.ToolBatch.Completion = nil
		}},
		{"result before source", func(v *agentexecution.ExecutionView) {
			*v = completedBatchView()
			v.ToolBatch.Completion.LastResultSequence = v.ToolBatch.Output.Event.Sequence
		}},
		{"two pending outputs", func(v *agentexecution.ExecutionView) {
			*v = completedBatchView()
			o := output(*v)
			v.OutputLimit = &o
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			view := baseView()
			test.change(&view)
			_, err := agentexecution.SelectNext(view, decisionTime())
			require.ErrorIs(t, err, agentexecution.ErrInvalidState)
		})
	}
}

func FuzzSelectNext(f *testing.F) {
	for mode := range byte(6) {
		for flags := range byte(16) {
			f.Add(mode, flags)
		}
	}
	f.Fuzz(func(t *testing.T, mode, flags byte) {
		makeView := func() agentexecution.ExecutionView {
			var view agentexecution.ExecutionView
			switch mode % 6 {
			case 0:
				view = initialView()
			case 1:
				view = retryView()
			case 2:
				view = completedBatchView()
			case 3:
				view = batchView()
			case 4:
				view = batchView()
				view.ToolBatch.HasRunnable = false
			case 5:
				view = compactionView()
			}
			view.Inputs.Steering, view.Inputs.Queued = flags&1 != 0, flags&2 != 0
			if flags&4 != 0 {
				view.StopSequence = 40
			}
			if flags&8 != 0 {
				view.State = agentexecution.AgentArchived
			}
			return view
		}
		view, original := makeView(), makeView()
		got, err := agentexecution.SelectNext(view, decisionTime())
		require.NoError(t, err)
		require.Equal(t, original, view)
		again, err := agentexecution.SelectNext(view, decisionTime())
		require.NoError(t, err)
		require.Equal(t, got, again)
		if got.IncompleteTools {
			require.Nil(t, got.Model)
			require.Equal(t, agentexecution.AdmitNone, got.Admission)
		}
		if got.Work == agentexecution.WorkModel {
			require.False(t, got.Model.ReadyAt.After(decisionTime()))
			later, err := agentexecution.SelectNext(view, decisionTime().Add(time.Second))
			require.NoError(t, err)
			require.Equal(t, got.Model, later.Model)
		}
	})
}
