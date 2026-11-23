//go:build integration

package httpapi

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

// A built-in call whose server stopped while running it is not run again when its key is offered:
// like a model's call, it may have taken effect, so the reaper fails it and the offer gets that.
func TestDaemonSocketProgramToolCallDoesNotRerunALostBuiltInCall(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx, store := t.Context(), f.project.Store
	offer := programToolOffer(f.process.ProcessID, "lost", toolcatalog.ToolNameStorageList, `{}`)
	record, err := store.Execution().CreateProgramToolCall(ctx, executionstore.CreateProgramToolCallInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID, ProcessID: f.process.ProcessUUID,
		Key: offer.Key, Name: offer.Name, Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{}`)})
	require.NoError(t, err)
	// Another server claimed the call and started running it, then stopped.
	ref := executionstore.ProgramToolCallRef{ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID,
		ProcessID: f.process.ProcessUUID, ToolCallID: record.ID, ClaimID: uuid.New()}
	_, found, err := store.Execution().ClaimProgramToolCall(ctx, ref, time.Minute)
	require.NoError(t, err)
	require.True(t, found)
	authorized, err := store.Execution().AuthorizeProgramToolCall(ctx, ref, time.Minute)
	require.NoError(t, err)
	require.Equal(t, executionstore.ToolCallStateWaiting, authorized.State)
	_, err = f.pool.Exec(ctx, `UPDATE tool_calls SET program_claim_expires_at=statement_timestamp()-interval '1 second'
 WHERE id=$1`, record.ID)
	require.NoError(t, err)

	socket := f.socket()
	require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
	select {
	case outbound := <-socket.send:
		t.Fatalf("the lost call was answered before it was settled: %+v", outbound.msg.ToolCallReport)
	case <-time.After(4 * programToolCallFallbackPoll):
	}
	still, found, err := store.Execution().GetProgramToolCall(ctx, f.project.ProjectUUID, f.process.AgentUUID,
		f.process.ProcessUUID, record.ID)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, executionstore.ToolCallStateWaiting, still.State, "the offer must not claim the call again")

	reaped, err := store.Execution().ReapProgramToolCalls(ctx, 10)
	require.NoError(t, err)
	require.Equal(t, 1, reaped)
	report := nextToolCallReport(t, socket)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, report.State)
	require.Equal(t, daemonprotocol.ProgramToolCallOutcomeFailed, report.Outcome)
	require.Len(t, report.Content, 1)
	require.Contains(t, string(report.Content[0].Value), `"error_code":"outcome_unknown"`)
	socket.toolCallsDone.Wait()
}
