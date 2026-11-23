//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func (f *daemonProgramToolCallFixture) storedCall(t *testing.T, key string) (
	daemonprotocol.ToolCallOffer, executionstore.ProgramToolCallRecord,
) {
	t.Helper()
	offer := programToolOffer(f.process.ProcessID, key, toolcatalog.ToolNameStorageList, `{}`)
	execution := f.project.Store.Execution()
	record, err := execution.CreateProgramToolCall(t.Context(), executionstore.CreateProgramToolCallInput{
		ProjectID: f.project.ProjectUUID, AgentID: f.process.AgentUUID, ProcessID: f.process.ProcessUUID,
		Key: offer.Key, Name: offer.Name, Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{}`)})
	require.NoError(t, err)
	return offer, record
}

func (f *daemonProgramToolCallFixture) toolCallState(t *testing.T, id string) (string, string) {
	t.Helper()
	uuid, err := publicid.Decode(publicid.KindToolCall, id)
	require.NoError(t, err)
	var state, outcome string
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT c.state, coalesce(r.outcome, '') FROM tool_calls c
 LEFT JOIN tool_call_results r ON r.tool_call_id = c.id WHERE c.id = $1`, uuid).Scan(&state, &outcome))
	return state, outcome
}

// A cancel that arrives while every slot is taken waits for one on the server, then ends its call
// and is acknowledged, without the daemon sending it again.
func TestDaemonSocketToolCallCancelWaitsForAFreeSlot(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	storedOffer, stored := f.storedCall(t, "stored")
	storedCancel := daemonprotocol.ToolCallCancel{ProcessID: storedOffer.ProcessID, Key: storedOffer.Key,
		OfferID: "earlier-connection"}

	blocked, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocked.Rollback(context.WithoutCancel(ctx)) }()
	_, err = blocked.Exec(ctx, `LOCK TABLE processes IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	socket := f.socket()
	for i := range daemonToolCallLimit {
		offer := programToolOffer(f.process.ProcessID, "k-"+strconv.Itoa(i), toolcatalog.ToolNameStorageList, `{}`)
		offer.OfferID = strconv.Itoa(i)
		require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
	}
	for range 2 {
		require.NoError(t, socket.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &storedCancel}))
	}
	socket.workMu.Lock()
	active, queued := socket.toolCallsActive, len(socket.toolCallCancelsQueued)
	socket.workMu.Unlock()
	require.Equal(t, daemonToolCallLimit, active)
	require.Equal(t, 1, queued, "a cancel sent again while it waits is held once")
	require.Empty(t, socket.send, "a held cancel is not acknowledged yet")

	require.NoError(t, blocked.Rollback(ctx))
	acknowledged := 0
	for completed := 0; completed < daemonToolCallLimit || acknowledged == 0; {
		report := nextToolCallReport(t, socket)
		switch {
		case report.CancelRecorded:
			require.Equal(t, []string{storedCancel.Key, storedCancel.OfferID}, []string{report.Key, report.OfferID})
			acknowledged++
		case report.State == daemonprotocol.ProgramToolCallStateCompleted,
			// The stored call counts against the command's limit too.
			report.ErrorCode == daemonprotocol.ErrorCodeToolCallLimitReached:
			completed++
		default:
			t.Fatalf("unexpected report %+v", report)
		}
	}
	socket.toolCallsDone.Wait()
	require.Equal(t, 1, acknowledged)
	require.Empty(t, socket.send)
	socket.workMu.Lock()
	active, queued = socket.toolCallsActive, len(socket.toolCallCancelsQueued)
	socket.workMu.Unlock()
	require.Zero(t, active)
	require.Zero(t, queued)
	state, outcome := f.toolCallState(t, testPublicID(t, publicid.KindToolCall, stored.ID))
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
}
