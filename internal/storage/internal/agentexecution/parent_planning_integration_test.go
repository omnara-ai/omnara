//go:build integration

package agentexecution_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/stretchr/testify/require"
)

func TestPredictableParentEffectsUseOneAttempt(t *testing.T) {
	for _, childFirst := range []bool{false, true} {
		for _, kind := range []string{
			"fused", "output", "tool_source", "refusal", "content_filter", "failure", "compaction_failure", "question",
		} {
			t.Run(map[bool]string{true: "child_first", false: "parent_first"}[childFirst]+"/"+kind, func(t *testing.T) {
				f, u, parent, child, lease := familyFixture(t, childFirst)
				pr, err := parent.Route()
				require.NoError(t, err)
				cr, err := child.Route()
				require.NoError(t, err)
				var prepared agentexecution.PreparedModel
				var tool agentexecution.ToolRef
				if kind == "question" {
					tool = toolCommand(t, child, lease, "built_in")
					_, err = child.AuthorizeTool(t.Context(), tool)
					require.NoError(t, err)
				} else {
					receiveCommand(t, child, "queued", "work")
					_, err = child.AdmitInputs(t.Context())
					require.NoError(t, err)
					prepared = prepareCommand(t, child, lease)
					if kind == "compaction_failure" {
						compaction, err := child.BeginCompaction(t.Context(), agentexecution.BeginCompactionInput{
							SourceEnd: prepared.Context.InputEventSequence,
							Failure: agentexecution.ModelFailure{ContextID: prepared.Context.ID, RuntimeLockID: lease,
								ErrorKind: "context_length", ErrorMessage: "compact"},
						})
						require.NoError(t, err)
						prepared = compaction.Model
					}
				}
				require.NoError(t, u.Commit(t.Context(), "prepare parent effect"))
				counter := &parentEffectAttempts{}
				config := f.pool.Config()
				config.ConnConfig.Tracer = counter
				pool, err := pgxpool.NewWithConfig(t.Context(), config)
				require.NoError(t, err)
				defer pool.Close()
				store := storage.NewStore(pool).Execution()
				output := executionstore.RecordModelOutputAndCompleteContextInput{
					ProjectID: f.project, AgentID: cr.AgentID, RuntimeLockID: lease,
					ModelCallContextID: prepared.Context.ID, ProviderResponse: commandResponse(modelenvelope.StopReasonEndTurn),
				}
				switch kind {
				case "fused":
					transition, acceptErr := store.AcceptModelOutputAndAdvance(t.Context(),
						executionstore.AcceptModelOutputAndAdvanceInput{Output: output})
					err = acceptErr
					require.Nil(t, transition.Fallback)
				case "output", "refusal", "content_filter":
					if kind == "refusal" {
						output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonRefusal
					} else if kind == "content_filter" {
						output.ProviderResponse.Normalized.StopReason = modelenvelope.StopReasonContentFilter
					}
					_, err = store.RecordModelOutputAndCompleteContext(t.Context(), output)
				case "tool_source":
					_, _, err = store.RecordToolCallSourceAndCompleteContext(t.Context(),
						executionstore.RecordToolCallSourceAndCompleteContextInput{
							ProjectID: f.project, AgentID: cr.AgentID, RuntimeLockID: lease,
							ModelCallContextID: prepared.Context.ID, ProviderResponse: output.ProviderResponse,
						})
				case "failure":
					_, err = store.RecordModelCallErrorAndCompleteContext(t.Context(),
						executionstore.RecordModelCallErrorAndCompleteContextInput{
							ProjectID: f.project, AgentID: cr.AgentID, RuntimeLockID: lease,
							ModelCallContextID: prepared.Context.ID, ErrorKind: "runtime", ErrorMessage: "failed",
						})
				case "compaction_failure":
					err = store.RecordTerminalCompactionFailure(t.Context(), executionstore.RecordTerminalCompactionFailureInput{
						ProjectID: f.project, AgentID: cr.AgentID, RuntimeLockID: lease,
						ModelCallContextID: prepared.Context.ID, ErrorKind: "runtime", ErrorMessage: "failed",
					})
				case "question":
					_, err = store.ExecuteToolCall(t.Context(), executionstore.ExecuteToolCallInput{
						ProjectID: f.project, AgentID: cr.AgentID, RuntimeLockID: lease, ToolCallID: tool.ID,
					}, func(*executionstore.ToolCallReader) (executionstore.ToolCallCommand, error) {
						return executionstore.CreateQuestionForToolCall(executionstore.CreateQuestionInteractionInput{
							Form: interactionform.Form{Title: "Question", Questions: []interactionform.Question{
								{Prompt: "Choose", Options: []interactionform.Option{{Label: "yes"}}},
							}},
						}), nil
					})
				}
				require.NoError(t, err)
				var effects int
				require.NoError(t, f.pool.QueryRow(t.Context(),
					`SELECT count(*) FROM agent_inputs WHERE agent_id=$1 AND input_kind='content'`, pr.AgentID).Scan(&effects))
				require.Equal(t, 1, effects)
				t.Logf("transaction attempts=%d", counter.begins)
				require.Equal(t, 1, counter.begins)
			})
		}
	}
}

type parentEffectAttempts struct{ begins int }

func (c *parentEffectAttempts) TraceQueryStart(
	ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData,
) context.Context {
	if data.SQL == "begin" {
		c.begins++
	}
	return ctx
}

func (*parentEffectAttempts) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
