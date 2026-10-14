//go:build integration

package kernel

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/stretchr/testify/require"
)

func TestAgentExecutorFailedPersistenceDoesNotNotify(t *testing.T) {
	t.Parallel()
	for _, kind := range []model.ErrorKind{model.ErrorKindTransient, model.ErrorKindInvalidRequest} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f := newKernelFixture(t, ctx)
			agentID, userID := f.createAgent(t, ctx, "openai/failure-persistence", f.Now)
			input := f.admitContentInputTurn(t, ctx, agentID, userID, "start", f.Now.Add(time.Millisecond))
			client := &sequenceKernelModel{
				providerModelSlug: "failure-persistence",
				afterPrepare:      f.Pool.Close,
				errs: []error{model.ProviderError{
					Kind: kind, Source: "fixture", Message: "model attempt failed",
				}},
			}
			notices := 0
			executor := AgentExecutor{
				Store: f.Store, ModelResolver: liveTestModelResolver(f.Store, client),
				OnModelFailure: func(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) error {
					notices++
					return nil
				},
			}
			require.Error(t, executor.ExecuteModelWork(ctx, input))
			require.Equal(t, 1, client.preparedCount())
			require.Equal(t, 1, client.respondedCount())
			require.Zero(t, notices, "an error without a committed failure must remain quiet")
		})
	}
}
