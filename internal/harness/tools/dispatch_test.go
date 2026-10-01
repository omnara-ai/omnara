package tools

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/stretchr/testify/require"
)

func TestToolCompletionRejectsUnsetContent(t *testing.T) {
	transactional := completeInTransaction(toolResultContent{})
	if failed, ok := transactional.(failTransaction); !ok || failed.cause == nil {
		t.Fatalf("transactional result = %#v, want failure", transactional)
	}

	async := completeAsynchronously(toolResultContent{})
	if failed, ok := async.(failAsync); !ok || failed.cause == nil {
		t.Fatalf("async result = %#v, want failure", async)
	}

	empty := newToolResultContent()
	if _, ok := completeInTransaction(empty).(completeTransaction); !ok {
		t.Fatal("explicitly empty transactional result was not accepted")
	}
	if _, ok := completeAsynchronously(empty).(completeAsync); !ok {
		t.Fatal("explicitly empty async result was not accepted")
	}
}

type backgroundAdmissionRunner struct {
	submit    func(string, func(context.Context) error) bool
	trySubmit func(string, func(context.Context) error) bool
}

func (r backgroundAdmissionRunner) Submit(label string, task func(context.Context) error) bool {
	return r.submit(label, task)
}

func (r backgroundAdmissionRunner) TrySubmit(label string, task func(context.Context) error) bool {
	return r.trySubmit(label, task)
}

func TestBackgroundToolAdmission(t *testing.T) {
	for _, test := range []struct {
		name                 string
		bestEffort, accepted bool
	}{
		{name: "required", accepted: true},
		{name: "best_effort", bestEffort: true, accepted: true},
		{name: "best_effort_full", bestEffort: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var scheduled func(context.Context) error
			var logs bytes.Buffer
			call := model.ToolCall{ID: "provider-call", Name: "test-tool"}
			toolCallID := uuid.New()
			interaction := executionstore.AgentInteractionRecord{ID: uuid.New()}
			submissions := 0
			admit := func(label string, task func(context.Context) error, bestEffort bool) bool {
				submissions++
				require.Equal(t, test.bestEffort, bestEffort, "only best-effort work may use TrySubmit")
				require.Equal(t, call.Name, label)
				if test.accepted {
					scheduled = task
				}
				return test.accepted
			}
			e := Executor{Log: slog.New(slog.NewTextHandler(&logs, nil)), BackgroundRunner: backgroundAdmissionRunner{
				submit:    func(label string, task func(context.Context) error) bool { return admit(label, task, false) },
				trySubmit: func(label string, task func(context.Context) error) bool { return admit(label, task, true) },
			}}
			failure := errors.New("presentation unavailable")
			e.submitBackgroundTool(Turn{}, call, toolHandler{BackgroundBestEffort: test.bestEffort,
				Background: func(ctx context.Context, background backgroundToolContext) error {
					_, bounded := ctx.Deadline()
					require.True(t, bounded)
					require.Equal(t, toolCallID, background.ToolCallID)
					require.Equal(t, interaction, background.CommandResult)
					return failure
				}}, toolCallID, interaction)
			require.Equal(t, 1, submissions)
			if test.accepted {
				require.NotNil(t, scheduled)
				require.ErrorIs(t, scheduled(t.Context()), failure, "the runner handles background failures")
				require.Empty(t, logs.String())
			} else {
				require.Nil(t, scheduled)
				require.Contains(t, logs.String(), "best-effort background tool dropped")
				require.Contains(t, logs.String(), toolCallID.String())
			}
		})
	}
}

func TestQuestionPresentationWithoutDestination(t *testing.T) {
	require.NoError(t, presentStructuredQuestion(t.Context(), backgroundToolContext{
		CommandResult: executionstore.AgentInteractionRecord{ID: uuid.New()},
	}))
}
