package tools

import (
	"context"
	"errors"
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

type backgroundRunnerFunc func(string, func(context.Context) error) bool

func (f backgroundRunnerFunc) Submit(label string, task func(context.Context) error) bool {
	return f(label, task)
}

func (f backgroundRunnerFunc) TrySubmit(label string, task func(context.Context) error) bool {
	return f(label, task)
}

func TestBackgroundToolContext(t *testing.T) {
	var scheduled func(context.Context) error
	call := model.ToolCall{ID: "provider-call", Name: "test-tool"}
	toolCallID := uuid.New()
	interaction := executionstore.AgentInteractionRecord{ID: uuid.New()}
	e := Executor{BackgroundRunner: backgroundRunnerFunc(func(label string, task func(context.Context) error) bool {
		require.Equal(t, call.Name, label)
		scheduled = task
		return true
	})}
	failure := errors.New("presentation unavailable")
	e.submitBackgroundTool(Turn{}, call, func(ctx context.Context, background backgroundToolContext) error {
		_, bounded := ctx.Deadline()
		require.True(t, bounded)
		require.Equal(t, toolCallID, background.ToolCallID)
		require.Equal(t, interaction, background.CommandResult)
		return failure
	}, toolCallID, interaction)
	require.NotNil(t, scheduled)
	require.ErrorIs(t, scheduled(t.Context()), failure, "the runner handles background failures")
}

func TestQuestionPresentationWithoutDestination(t *testing.T) {
	require.NoError(t, presentStructuredQuestion(t.Context(), backgroundToolContext{
		CommandResult: executionstore.AgentInteractionRecord{ID: uuid.New()},
	}))
}
