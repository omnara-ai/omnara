//go:build integration

package httpapi

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type countingProgramToolExecutor struct {
	ProgramToolExecutor
	authorized, executed *atomic.Int32
}

func (e countingProgramToolExecutor) RunProgramToolCall(
	ctx context.Context, claim executionstore.ProgramToolCallClaim, lease time.Duration,
) error {
	switch claim.Phase {
	case executionstore.ProgramToolCallPhaseAuthorize:
		e.authorized.Add(1)
	case executionstore.ProgramToolCallPhaseExecute:
		e.executed.Add(1)
	}
	return e.ProgramToolExecutor.RunProgramToolCall(ctx, claim, lease)
}

func TestDaemonSocketNamedToolCallRunsAfterItsOfferIsGone(t *testing.T) {
	t.Parallel()
	for _, approval := range []struct {
		name     string
		recovery time.Duration
		approve  func(t *testing.T, f *daemonProgramToolCallFixture, agentPath string, interaction uuid.UUID)
	}{
		{
			name: "recorded here", recovery: time.Hour,
			approve: func(t *testing.T, f *daemonProgramToolCallFixture, agentPath string, interaction uuid.UUID) {
				t.Helper()
				requestJSONWithHeaders(t, f.handler, http.MethodPost,
					agentPath+"/interactions/"+testPublicID(t, publicid.KindAgentInteraction, interaction)+"/resolve",
					httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK,
					authHeaders(f.project.AdminToken))
			},
		},
		{
			name: "recorded elsewhere", recovery: 100 * time.Millisecond,
			approve: func(t *testing.T, f *daemonProgramToolCallFixture, _ string, interaction uuid.UUID) {
				t.Helper()
				_, err := f.project.Store.Execution().ResolveAgentInteraction(t.Context(),
					executionstore.ResolveAgentInteractionInput{ProjectID: f.project.ProjectUUID,
						AgentID: f.process.AgentUUID, ID: interaction,
						Resolution: httpQuestionResolution(toolpermission.AllowOptionIndex),
						Actor:      httpOmnaraActorParams(t, f.project.OrgUUID, f.project.AdminUserUUID)})
				require.NoError(t, err)
			},
		},
	} {
		t.Run(approval.name, func(t *testing.T) {
			t.Parallel()
			var authorized, executed atomic.Int32
			f := newDaemonProgramToolCallFixture(t,
				func(store *storage.Store) Option {
					return WithProgramToolExecutor(countingProgramToolExecutor{
						ProgramToolExecutor: tools.Executor{Store: store}, authorized: &authorized, executed: &executed})
				},
				func(*storage.Store) Option { return withProgramToolRecoveryInterval(approval.recovery) })
			f.acceptProcess(t)
			ctx, execution := t.Context(), f.project.Store.Execution()
			ask := programToolOffer(f.process.ProcessID, "named-ask", toolcatalog.ToolNameStorageRead,
				`{"path":"/memory/engineering/notes.md"}`)
			ask.OfferID, ask.KeepOnDisconnect = "first", true

			waiting := f.socket()
			waitCtx, timeOut := context.WithCancel(ctx)
			require.NoError(t, waiting.handleToolCallOffer(waitCtx, daemonprotocol.Message{ToolCallOffer: &ask}))
			report := nextToolCallReport(t, waiting)
			require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, report.State)
			timeOut()
			waiting.toolCallsDone.Wait()
			callID, err := publicid.Decode(publicid.KindToolCall, report.ToolCallID)
			require.NoError(t, err)
			var interaction uuid.UUID
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT id FROM agent_interactions
 WHERE agent_id=$1 AND tool_call_id=$2 AND state='open'`, f.process.AgentUUID, callID).Scan(&interaction))
			require.EqualValues(t, 1, authorized.Load())
			require.Zero(t, executed.Load())

			agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
				testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
			approval.approve(t, f, agentPath, interaction)
			var done executionstore.ProgramToolCallRecord
			require.Eventually(t, func() bool {
				var found bool
				done, found, err = execution.GetProgramToolCall(ctx, f.project.ProjectUUID, f.process.AgentUUID,
					f.process.ProcessUUID, callID)
				require.NoError(t, err)
				require.True(t, found)
				return done.State == executionstore.ToolCallStateCompleted
			}, 15*time.Second, 50*time.Millisecond, "the approved call runs with no offer waiting for it")
			require.Equal(t, executionstore.ToolResultOutcomeFailed, done.Outcome)
			require.Contains(t, string(done.ResultContentParts), "memory store is unavailable",
				"the tool ran, rather than the call being ended without running")
			require.EqualValues(t, 1, executed.Load(), "the original call runs once")

			rerun := ask
			rerun.OfferID = "second"
			result := f.socket()
			require.NoError(t, result.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &rerun}))
			replay := nextToolCallReport(t, result)
			result.toolCallsDone.Wait()
			require.Equal(t, "second", replay.OfferID)
			require.Equal(t, report.ToolCallID, replay.ToolCallID, "a rerun gets the original call")
			require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, replay.State)
			require.Equal(t, daemonprotocol.ProgramToolCallOutcomeFailed, replay.Outcome)
			require.Contains(t, string(structuredResult(t, replay.ProgramToolCallResult)), "memory store is unavailable")
			var results int
			require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM tool_call_results WHERE tool_call_id=$1`,
				callID).Scan(&results))
			require.Equal(t, 1, results)
			require.EqualValues(t, 1, executed.Load(), "the rerun does not run the call again")
			require.EqualValues(t, 1, authorized.Load())
		})
	}
}
