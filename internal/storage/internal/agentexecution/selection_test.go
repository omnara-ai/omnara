package agentexecution_test

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func identity(n byte) uuid.UUID { return uuid.UUID{15: n} }

func decisionTime() time.Time { return time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC) }

func baseView() agentexecution.ExecutionView {
	opening := agentexecution.Opening{InputIDs: []uuid.UUID{identity(3)}, EventSequence: 10}
	return agentexecution.ExecutionView{
		AgentID: identity(1), State: agentexecution.AgentActive,
		MaxNormalInputSequence: 10, MaxContextInputSequence: 10,
		Turn: &agentexecution.Turn{
			ID: identity(2), FirstOpeningSequence: 10, LastOpeningSequence: 10, FirstContentSequence: 10,
			LatestSemantic: agentexecution.EventBoundary{Sequence: 10, Time: decisionTime().Add(-time.Minute)},
			InitialOpening: opening, InitialReadyAt: decisionTime().Add(-time.Minute),
		},
		NormalContext: &agentexecution.ModelContext{
			ID: identity(4), AgentID: identity(1), TurnID: identity(2), Operation: agentexecution.OperationNormal,
			Attempt: 1, InputEventSequence: 10, State: agentexecution.ContextSucceeded, Opening: opening,
		},
	}
}

func initialView() agentexecution.ExecutionView {
	view := baseView()
	view.NormalContext = nil
	view.MaxNormalInputSequence, view.MaxContextInputSequence = 0, 0
	return view
}

func retryView() agentexecution.ExecutionView {
	view := baseView()
	deadline := decisionTime().Add(-time.Second)
	view.NormalContext.State, view.NormalContext.Recovery = agentexecution.ContextFailed, agentexecution.RecoveryRetry
	view.NormalContext.RetryAt = &deadline
	return view
}

func boundary(view agentexecution.ExecutionView, id byte, sequence int64) *agentexecution.BoundaryWork {
	return &agentexecution.BoundaryWork{
		ID: identity(id), AgentID: view.AgentID, TurnID: view.Turn.ID,
		Event:   agentexecution.EventBoundary{Sequence: sequence, Time: decisionTime().Add(-time.Second)},
		Opening: view.Turn.InitialOpening,
	}
}

func output(view agentexecution.ExecutionView) agentexecution.ModelOutput {
	return agentexecution.ModelOutput{
		ID: identity(5), EventID: identity(6), Context: *view.NormalContext,
		Event: agentexecution.EventBoundary{Sequence: 20, Time: decisionTime().Add(-time.Second)},
	}
}

func batchView() agentexecution.ExecutionView {
	view := baseView()
	view.ToolBatch = &agentexecution.ToolBatch{
		Output: output(view), HasIncomplete: true, HasRunnable: true,
	}
	return view
}

func completedBatchView() agentexecution.ExecutionView {
	view := batchView()
	view.ToolBatch.HasIncomplete, view.ToolBatch.HasRunnable = false, false
	view.ToolBatch.Completion = &agentexecution.BatchCompletion{
		LastResultSequence: 30, ReadyAt: decisionTime().Add(-time.Second),
	}
	return view
}

func compactionView() agentexecution.ExecutionView {
	view := retryView()
	view.NormalContext.Recovery, view.NormalContext.RetryAt = agentexecution.RecoveryCompact, nil
	compaction := *view.NormalContext
	compaction.ID, compaction.Operation = identity(7), agentexecution.OperationCompaction
	compaction.SourceEventSequenceEnd = 9
	compaction.State, compaction.Recovery = agentexecution.ContextStarted, agentexecution.RecoveryNone
	view.CompactionContext = &compaction
	return view
}

func TestSelectNextPrecedence(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name        string
		view        func() agentexecution.ExecutionView
		change      func(*agentexecution.ExecutionView)
		work        agentexecution.WorkKind
		admit       agentexecution.AdmissionKind
		wait        agentexecution.WaitKind
		origin      agentexecution.CandidateOrigin
		continuable bool
		incomplete  bool
	}{
		{name: "idle", view: baseView, wait: agentexecution.WaitIdle},
		{name: "initial", view: initialView, work: agentexecution.WorkModel,
			origin: agentexecution.OriginInitial, continuable: true},
		{name: "retry", view: retryView, work: agentexecution.WorkModel,
			origin: agentexecution.OriginRetry, continuable: true},
		{name: "later semantic", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.Turn.LatestSemantic = agentexecution.EventBoundary{Sequence: 15, Time: decisionTime().Add(-time.Second)}
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginSemantic, continuable: true},
		{name: "config", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.Config = boundary(*v, 8, 15)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginConfig, continuable: true},
		{name: "completed tools", view: completedBatchView, work: agentexecution.WorkModel,
			origin: agentexecution.OriginTools, continuable: true},
		{name: "output limit", view: baseView, change: func(v *agentexecution.ExecutionView) {
			o := output(*v)
			v.OutputLimit = &o
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginOutputLimit, continuable: true},
		{name: "checkpoint", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.Checkpoint = boundary(*v, 9, 15)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginCheckpoint, continuable: true},
		{name: "all steering before queued", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs = agentexecution.InputAvailability{Steering: true, Queued: true}
		}, work: agentexecution.WorkInput, admit: agentexecution.AdmitAllSteering},
		{name: "one queued", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs.Queued = true
		}, work: agentexecution.WorkInput, admit: agentexecution.AdmitOneQueued},
		{name: "tools before inputs", view: batchView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs = agentexecution.InputAvailability{Steering: true, Queued: true}
		}, work: agentexecution.WorkTool, continuable: true, incomplete: true},
		{name: "tools before config and checkpoint", view: batchView, change: func(v *agentexecution.ExecutionView) {
			v.Config, v.Checkpoint = boundary(*v, 8, 21), boundary(*v, 9, 22)
		}, work: agentexecution.WorkTool, continuable: true, incomplete: true},
		{name: "external tools block everything", view: batchView, change: func(v *agentexecution.ExecutionView) {
			v.ToolBatch.HasRunnable = false
			v.Inputs = agentexecution.InputAvailability{Steering: true, Queued: true}
			v.Config, v.Checkpoint = boundary(*v, 8, 21), boundary(*v, 9, 22)
		}, wait: agentexecution.WaitToolBatch, continuable: true, incomplete: true},
		{name: "steering before initial", view: initialView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs.Steering = true
		}, work: agentexecution.WorkInput, admit: agentexecution.AdmitAllSteering,
			origin: agentexecution.OriginInitial, continuable: true},
		{name: "steering retains retry candidate", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs.Steering = true
			deadline := decisionTime().Add(time.Hour)
			v.NormalContext.RetryAt = &deadline
		}, work: agentexecution.WorkInput, admit: agentexecution.AdmitAllSteering,
			origin: agentexecution.OriginRetry, continuable: true},
		{name: "model before queued", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs.Queued = true
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginRetry, continuable: true},
		{name: "future retry blocks queued", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.Inputs.Queued = true
			deadline := decisionTime().Add(time.Hour)
			v.NormalContext.RetryAt = &deadline
		}, wait: agentexecution.WaitModelDeadline, origin: agentexecution.OriginRetry, continuable: true},
		{name: "initial before config", view: initialView, change: func(v *agentexecution.ExecutionView) {
			v.Config = boundary(*v, 8, 15)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginInitial, continuable: true},
		{name: "semantic before config", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.Config = boundary(*v, 8, 15)
			v.Turn.LatestSemantic = v.Config.Event
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginSemantic, continuable: true},
		{name: "config before completed batch", view: completedBatchView, change: func(v *agentexecution.ExecutionView) {
			v.Config = boundary(*v, 8, 31)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginConfig, continuable: true},
		{name: "config before output limit", view: baseView, change: func(v *agentexecution.ExecutionView) {
			o := output(*v)
			v.OutputLimit, v.Config = &o, boundary(*v, 8, 21)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginConfig, continuable: true},
		{name: "config before checkpoint", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.Config, v.Checkpoint = boundary(*v, 8, 15), boundary(*v, 9, 16)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginConfig, continuable: true},
		{name: "completed batch before checkpoint", view: completedBatchView, change: func(v *agentexecution.ExecutionView) {
			v.Checkpoint = boundary(*v, 9, 31)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginTools, continuable: true},
		{name: "future retry before completed batch and checkpoint", view: completedBatchView,
			change: func(v *agentexecution.ExecutionView) {
				deadline := decisionTime().Add(time.Hour)
				v.NormalContext.ID, v.NormalContext.InputEventSequence = identity(12), 30
				v.MaxNormalInputSequence, v.MaxContextInputSequence = 30, 30
				v.NormalContext.State, v.NormalContext.Recovery = agentexecution.ContextFailed, agentexecution.RecoveryRetry
				v.NormalContext.RetryAt = &deadline
				v.Checkpoint = boundary(*v, 9, 31)
				v.Inputs.Queued = true
			}, wait: agentexecution.WaitModelDeadline, origin: agentexecution.OriginRetry, continuable: true},
		{name: "output limit before checkpoint", view: baseView, change: func(v *agentexecution.ExecutionView) {
			o := output(*v)
			v.OutputLimit, v.Checkpoint = &o, boundary(*v, 9, 21)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginOutputLimit, continuable: true},
		{name: "empty initial falls through", view: initialView, change: func(v *agentexecution.ExecutionView) {
			v.Turn.InitialOpening = agentexecution.Opening{}
			v.Config = boundary(*v, 8, 15)
			v.Config.Opening = baseView().Turn.InitialOpening
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginConfig, continuable: true},
		{name: "empty config falls through", view: completedBatchView, change: func(v *agentexecution.ExecutionView) {
			v.Config = boundary(*v, 8, 31)
			v.Config.Opening = agentexecution.Opening{}
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginTools, continuable: true},
		{name: "empty retry falls through", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.NormalContext.Opening = agentexecution.Opening{}
			v.Checkpoint = boundary(*v, 9, 31)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginCheckpoint, continuable: true},
		{name: "empty semantic falls through", view: retryView, change: func(v *agentexecution.ExecutionView) {
			v.NormalContext.Opening = agentexecution.Opening{}
			v.Config = boundary(*v, 8, 31)
			v.Turn.LatestSemantic = v.Config.Event
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginConfig, continuable: true},
		{name: "empty tools fall through", view: completedBatchView, change: func(v *agentexecution.ExecutionView) {
			v.ToolBatch.Output.Context.Opening = agentexecution.Opening{}
			v.Checkpoint = boundary(*v, 9, 31)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginCheckpoint, continuable: true},
		{name: "empty output limit falls through", view: baseView, change: func(v *agentexecution.ExecutionView) {
			o := output(*v)
			o.Context.Opening = agentexecution.Opening{}
			v.OutputLimit, v.Checkpoint = &o, boundary(*v, 9, 31)
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginCheckpoint, continuable: true},
		{name: "empty checkpoint falls through to queued", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.Checkpoint = boundary(*v, 9, 31)
			v.Checkpoint.Opening = agentexecution.Opening{}
			v.Inputs.Queued = true
		}, work: agentexecution.WorkInput, admit: agentexecution.AdmitOneQueued},
		{name: "config only with unanswered prior inputs", view: initialView, change: func(v *agentexecution.ExecutionView) {
			v.Turn.FirstContentSequence = 0
			v.Turn.InitialOpening.EventSequence = 2
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginInitial, continuable: true},
		{name: "config only without unanswered inputs", view: initialView, change: func(v *agentexecution.ExecutionView) {
			v.Turn.FirstContentSequence = 0
			v.Turn.InitialOpening = agentexecution.Opening{}
			v.Config = boundary(*v, 8, 10)
		}, wait: agentexecution.WaitIdle},
		{name: "started context is continuable without work", view: baseView, change: func(v *agentexecution.ExecutionView) {
			v.NormalContext.State = agentexecution.ContextStarted
		}, wait: agentexecution.WaitIdle, continuable: true},
		{name: "compaction suppresses normal recovery", view: compactionView,
			wait: agentexecution.WaitIdle, continuable: true},
		{name: "terminal compaction suppresses normal recovery", view: compactionView,
			change: func(v *agentexecution.ExecutionView) { v.CompactionContext.State = agentexecution.ContextSucceeded },
			wait:   agentexecution.WaitIdle},
		{name: "compaction retry", view: compactionView, change: func(v *agentexecution.ExecutionView) {
			deadline := decisionTime()
			v.CompactionContext.State = agentexecution.ContextFailed
			v.CompactionContext.Recovery, v.CompactionContext.RetryAt = agentexecution.RecoveryRetry, &deadline
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginRetry, continuable: true},
		{name: "compaction semantic restart", view: compactionView, change: func(v *agentexecution.ExecutionView) {
			v.Turn.LatestSemantic.Sequence = 15
		}, work: agentexecution.WorkModel, origin: agentexecution.OriginSemantic, continuable: true},
		{name: "archived suppresses work", view: batchView, change: func(v *agentexecution.ExecutionView) {
			v.State = agentexecution.AgentArchived
			v.Inputs.Steering = true
		}, wait: agentexecution.WaitArchived},
		{name: "no turn admits content", view: func() agentexecution.ExecutionView {
			return agentexecution.ExecutionView{AgentID: identity(1), State: agentexecution.AgentActive,
				Inputs: agentexecution.InputAvailability{Queued: true}}
		}, work: agentexecution.WorkInput, admit: agentexecution.AdmitOneQueued},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			view := test.view()
			if test.change != nil {
				test.change(&view)
			}
			got, err := agentexecution.SelectNext(view, decisionTime())
			require.NoError(t, err)
			require.Equal(t, test.work, got.Work)
			require.Equal(t, test.admit, got.Admission)
			require.Equal(t, test.wait, got.Wait)
			require.Equal(t, test.continuable, got.TurnContinuable)
			require.Equal(t, test.incomplete, got.IncompleteTools)
			if test.origin == 0 {
				require.Nil(t, got.Model)
			} else {
				require.NotNil(t, got.Model)
				require.Equal(t, test.origin, got.Model.Origin)
				require.Equal(t, view.Turn.ID, got.Model.TurnID)
				kind := agentexecution.ModelStart
				if test.origin == agentexecution.OriginRetry {
					kind = agentexecution.ModelResume
				} else if test.origin == agentexecution.OriginTools || test.origin == agentexecution.OriginOutputLimit {
					kind = agentexecution.ModelContinue
				}
				require.Equal(t, kind, got.Model.Kind)
			}
			if got.Work != agentexecution.WorkNone {
				require.NotNil(t, got.LogicalReadyAt)
				require.False(t, got.LogicalReadyAt.After(decisionTime()))
			} else if test.wait != agentexecution.WaitModelDeadline {
				require.Nil(t, got.LogicalReadyAt)
			}
		})
	}
}

func TestSelectNextStrictBoundaries(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"initial stop", "context stop", "batch stop", "config stop", "checkpoint stop",
		"output limit stop", "initial watermark", "config watermark", "checkpoint watermark", "output watermark",
		"semantic watermark", "retry deadline", "config first content"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			for _, offset := range []int64{-1, 0, 1} {
				view := baseView()
				want := offset <= 0
				switch kind {
				case "initial stop":
					view = initialView()
					view.StopSequence = 10 + offset
				case "context stop":
					view = retryView()
					view.StopSequence = 10 + offset
				case "batch stop":
					view = completedBatchView()
					view.StopSequence = 20 + offset
				case "config stop":
					view.Config = boundary(view, 8, 20)
					view.StopSequence = 20 + offset
				case "checkpoint stop":
					view.Checkpoint = boundary(view, 9, 20)
					view.StopSequence = 20 + offset
				case "output limit stop":
					o := output(view)
					view.OutputLimit, view.StopSequence = &o, 20+offset
				case "initial watermark":
					view = initialView()
					view.MaxContextInputSequence = 10 + offset
					want = offset < 0
				case "config watermark":
					view.Config = boundary(view, 8, 10+offset)
					want = offset > 0
				case "checkpoint watermark":
					view.Checkpoint = boundary(view, 9, 10+offset)
					want = offset > 0
				case "output watermark":
					o := output(view)
					view.OutputLimit, view.NormalContext = &o, nil
					view.MaxNormalInputSequence, view.MaxContextInputSequence = 20+offset, 20+offset
					want = offset < 0
				case "semantic watermark":
					view.NormalContext.State = agentexecution.ContextStarted
					view.Turn.LatestSemantic.Sequence = 10 + offset
					want = offset > 0
				case "retry deadline":
					view = retryView()
					deadline := decisionTime().Add(time.Duration(offset) * time.Nanosecond)
					view.NormalContext.RetryAt = &deadline
				case "config first content":
					view.Turn.FirstContentSequence = 20
					view.Config = boundary(view, 8, 20+offset)
					want = offset >= 0
				}
				got, err := agentexecution.SelectNext(view, decisionTime())
				require.NoError(t, err, "offset %d", offset)
				require.Equal(t, want, got.Work == agentexecution.WorkModel, "offset %d: %+v", offset, got)
			}
		})
	}
}

func TestSelectNextPreservesSourceLineage(t *testing.T) {
	t.Parallel()
	for _, origin := range []agentexecution.CandidateOrigin{
		agentexecution.OriginTools, agentexecution.OriginOutputLimit, agentexecution.OriginRetry,
		agentexecution.OriginSemantic, agentexecution.OriginConfig, agentexecution.OriginCheckpoint,
	} {
		view := completedBatchView()
		inherited := agentexecution.Opening{InputIDs: []uuid.UUID{identity(11), identity(3)}, EventSequence: 2}
		view.NormalContext.Opening = inherited
		view.ToolBatch.Output.Context.Opening = inherited
		switch origin {
		case agentexecution.OriginTools:
		case agentexecution.OriginOutputLimit:
			o := view.ToolBatch.Output
			view.ToolBatch, view.OutputLimit = nil, &o
		case agentexecution.OriginRetry, agentexecution.OriginSemantic:
			view.ToolBatch = nil
			view.NormalContext.State, view.NormalContext.Recovery = agentexecution.ContextFailed, agentexecution.RecoveryRetry
			deadline := decisionTime()
			view.NormalContext.RetryAt = &deadline
			if origin == agentexecution.OriginSemantic {
				view.Turn.LatestSemantic.Sequence = 25
			}
		case agentexecution.OriginConfig:
			view.ToolBatch, view.Config = nil, boundary(view, 8, 25)
			view.Config.Opening = inherited
		case agentexecution.OriginCheckpoint:
			view.ToolBatch, view.Checkpoint = nil, boundary(view, 9, 25)
			view.Checkpoint.Opening = inherited
		default:
			t.Fatalf("unexpected test origin %d", origin)
		}
		got, err := agentexecution.SelectNext(view, decisionTime())
		require.NoError(t, err)
		require.NotNil(t, got.Model)
		require.Equal(t, origin, got.Model.Origin)
		require.Equal(t, inherited, got.Model.Opening)
		switch origin {
		case agentexecution.OriginConfig:
			require.Equal(t, identity(8), got.Model.SourceInputID)
		case agentexecution.OriginCheckpoint:
			require.Equal(t, identity(9), got.Model.SourceCheckpointID)
		default:
			require.Equal(t, identity(4), got.Model.SourceContextID)
		}
		if origin == agentexecution.OriginTools || origin == agentexecution.OriginOutputLimit {
			require.Equal(t, identity(5), got.Model.SourceOutputID)
		}
		got.Model.Opening.InputIDs[0] = identity(99)
		require.Equal(t, identity(11), inherited.InputIDs[0])
		again, err := agentexecution.SelectNext(view, decisionTime())
		require.NoError(t, err)
		require.Equal(t, inherited, again.Model.Opening)
	}
}
