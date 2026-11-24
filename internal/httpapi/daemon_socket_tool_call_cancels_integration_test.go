//go:build integration

package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
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
		Key: offer.Key, Name: offer.Name, Type: toolcatalog.ToolTypeBuiltIn, Input: json.RawMessage(`{}`),
		OfferID: key})
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

func TestDaemonSocketToolCallCancelWaitsForAFreeSlot(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	storedOffer, stored := f.storedCall(t, "stored")
	storedCancel := daemonprotocol.ToolCallCancel{ProcessID: storedOffer.ProcessID, Key: storedOffer.Key,
		OfferID: "stored"}

	blocked, err := f.pool.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = blocked.Rollback(context.WithoutCancel(ctx)) }()
	_, err = blocked.Exec(ctx, `LOCK TABLE processes IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	socket := f.socket()
	for i := range daemonToolCallCancelLimit {
		cancel := daemonprotocol.ToolCallCancel{
			ProcessID: f.process.ProcessID,
			Key:       programToolOffer(f.process.ProcessID, "c-"+strconv.Itoa(i), "", "").Key,
			OfferID:   "c-" + strconv.Itoa(i),
		}
		require.NoError(t, socket.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &cancel}))
	}
	for range 2 {
		require.NoError(t, socket.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &storedCancel}))
	}
	socket.workMu.Lock()
	active, queued := socket.toolCallCancelsActive, len(socket.toolCallCancelsQueued)
	socket.workMu.Unlock()
	require.Equal(t, daemonToolCallCancelLimit, active)
	require.Equal(t, 1, queued, "a cancel sent again while it waits is held once")
	require.Empty(t, socket.toolSend, "a held cancel is not acknowledged yet")

	require.NoError(t, blocked.Rollback(ctx))
	acknowledged := 0
	for completed := 0; completed < daemonToolCallCancelLimit || acknowledged == 0; {
		report := nextToolCallReport(t, socket)
		require.True(t, report.CancelRecorded, "unexpected report %+v", report)
		if report.Key == storedCancel.Key && report.OfferID == storedCancel.OfferID {
			acknowledged++
		} else {
			completed++
		}
	}
	socket.toolCallsDone.Wait()
	require.Equal(t, 1, acknowledged)
	require.Empty(t, socket.toolSend)
	socket.workMu.Lock()
	active, queued = socket.toolCallCancelsActive, len(socket.toolCallCancelsQueued)
	socket.workMu.Unlock()
	require.Zero(t, active)
	require.Zero(t, queued)
	state, outcome := f.toolCallState(t, testPublicID(t, publicid.KindToolCall, stored.ID))
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
}

func TestDaemonSocketToolCallCancelProgressesWhileOfferSlotsAreFull(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	approval := f.awaitingApproval(t, "old-approval")
	approvalOffer := programToolOffer(f.process.ProcessID, "old-approval", "", "")
	approvalOffer.OfferID = "old-approval"
	f.changeConfig(t, strings.Replace(programToolAgentYAML(true, "storage_list"), "code_mode:\n",
		customProgramToolsYAML+"code_mode:\n", 1))
	socket := f.socket()
	socketCtx, disconnectSocket := context.WithCancel(ctx)
	defer func() {
		disconnectSocket()
		socket.toolCallsDone.Wait()
	}()
	// The call awaiting approval counts toward the process's own limit, so the last socket slot is
	// taken directly.
	socket.workMu.Lock()
	socket.toolCallsActive++
	socket.workMu.Unlock()
	for i := range daemonToolCallLimit - 1 {
		offer := programToolOffer(f.process.ProcessID, "custom-"+strconv.Itoa(i), "create_ticket",
			`{"title":"t"}`)
		offer.KeepOnDisconnect = true
		offer.OfferID = "custom-" + strconv.Itoa(i)
		require.NoError(t, socket.handleToolCallOffer(socketCtx, daemonprotocol.Message{ToolCallOffer: &offer}))
		waiting := nextToolCallReport(t, socket)
		require.Empty(t, waiting.AckStatus, waiting.Error)
		require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingResult, waiting.State)
	}
	socket.workMu.Lock()
	activeOffers := socket.toolCallsActive
	socket.workMu.Unlock()
	require.Equal(t, daemonToolCallLimit, activeOffers)

	cancel := daemonprotocol.ToolCallCancel{
		ProcessID: approvalOffer.ProcessID, Key: approvalOffer.Key, OfferID: approvalOffer.OfferID,
	}
	require.NoError(t, socket.handleToolCallCancel(socketCtx, daemonprotocol.Message{ToolCallCancel: &cancel}))
	ack := nextToolCallReport(t, socket)
	require.True(t, ack.CancelRecorded)
	require.Equal(t, approvalOffer.Key, ack.Key)
	require.Equal(t, approvalOffer.OfferID, ack.OfferID)

	state, outcome := f.toolCallState(t, approval.ToolCallID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
	var openApprovals int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM agent_interactions WHERE agent_id = $1
 AND state = 'open'`, f.process.AgentUUID).Scan(&openApprovals))
	require.Zero(t, openApprovals)
}

func (f *daemonProgramToolCallFixture) awaitingApproval(t *testing.T, key string) daemonprotocol.ToolCallReport {
	t.Helper()
	offer := programToolOffer(f.process.ProcessID, key, toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/notes.md"}`)
	offer.OfferID = key
	socket := f.socket()
	socketCtx, disconnect := context.WithCancel(t.Context())
	require.NoError(t, socket.handleToolCallOffer(socketCtx, daemonprotocol.Message{ToolCallOffer: &offer}))
	waiting := nextToolCallReport(t, socket)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, waiting.State)
	disconnect()
	socket.toolCallsDone.Wait()
	return waiting
}

func (f *daemonProgramToolCallFixture) revokeToken(t *testing.T) {
	t.Helper()
	_, err := f.pool.Exec(t.Context(), `UPDATE machine_daemon_tokens SET revoked_at = statement_timestamp(),
 revoke_reason = 'test' WHERE id = $1`, f.process.TokenUUID)
	require.NoError(t, err)
}

func requireNoToolCallReport(t *testing.T, socket *daemonSocket) {
	t.Helper()
	socket.toolCallsDone.Wait()
	select {
	case outbound := <-socket.toolSend:
		t.Fatalf("unexpected report %+v", outbound.msg.ToolCallReport)
	default:
	}
}

func TestDaemonSocketToolCallCancelRequiresAnUnrevokedToken(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	stored := f.awaitingApproval(t, "stored")

	serving := f.socket()
	offer := programToolOffer(f.process.ProcessID, "serving", toolcatalog.ToolNameStorageRead,
		`{"path":"/memory/engineering/other.md"}`)
	offer.OfferID = "serving"
	require.NoError(t, serving.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
	served := nextToolCallReport(t, serving)
	require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, served.State)

	f.revokeToken(t)
	servingCancel := daemonprotocol.ToolCallCancel{ProcessID: offer.ProcessID, Key: offer.Key, OfferID: offer.OfferID}
	require.NoError(t, serving.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &servingCancel}))
	requireNoToolCallReport(t, serving)
	old := f.socket()
	storedCancel := daemonprotocol.ToolCallCancel{ProcessID: f.process.ProcessID,
		Key: programToolOffer(f.process.ProcessID, "stored", "", "").Key, OfferID: "stored"}
	require.NoError(t, old.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &storedCancel}))
	requireNoToolCallReport(t, old)
	for _, call := range []daemonprotocol.ToolCallReport{stored, served} {
		state, _ := f.toolCallState(t, call.ToolCallID)
		require.Equal(t, "awaiting_permission", state, "a revoked token must not end a call")
	}
}

func TestDaemonSocketToolCallCancelAllowsAnExpiredLease(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	stored := f.awaitingApproval(t, "stored")
	expireDaemonRuntimeForHTTPTest(t, t.Context(), f.pool, f.process.RuntimeUUID)
	socket := f.socket()
	cancel := daemonprotocol.ToolCallCancel{ProcessID: f.process.ProcessID,
		Key: programToolOffer(f.process.ProcessID, "stored", "", "").Key, OfferID: "stored"}
	require.NoError(t, socket.handleToolCallCancel(t.Context(), daemonprotocol.Message{ToolCallCancel: &cancel}))
	require.True(t, nextToolCallReport(t, socket).CancelRecorded)
	socket.toolCallsDone.Wait()
	state, outcome := f.toolCallState(t, stored.ToolCallID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
}

func TestDaemonSocketToolCallCancelEndsACallAfterCodeModeIsOff(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	stored := f.awaitingApproval(t, "stored")
	f.changeConfig(t, programToolAgentYAML(false, "storage_list", "storage_read"))
	f.rejected(t, "new", toolcatalog.ToolNameStorageList, `{}`, daemonprotocol.ErrorCodeToolCallUnavailable)

	socket := f.socket()
	cancel := daemonprotocol.ToolCallCancel{ProcessID: f.process.ProcessID,
		Key: programToolOffer(f.process.ProcessID, "stored", "", "").Key, OfferID: "stored"}
	require.NoError(t, socket.handleToolCallCancel(t.Context(), daemonprotocol.Message{ToolCallCancel: &cancel}))
	require.True(t, nextToolCallReport(t, socket).CancelRecorded)
	socket.toolCallsDone.Wait()
	state, outcome := f.toolCallState(t, stored.ToolCallID)
	require.Equal(t, []string{"completed", "canceled"}, []string{state, outcome})
	var open int
	require.NoError(t, f.pool.QueryRow(t.Context(), `SELECT count(*) FROM agent_interactions WHERE agent_id = $1
 AND state = 'open'`, f.process.AgentUUID).Scan(&open))
	require.Zero(t, open, "the call's approval is closed with it")
}

func TestDaemonSocketToolCallLateCancelLeavesARetryAlone(t *testing.T) {
	t.Parallel()
	f := newDaemonProgramToolCallFixture(t)
	f.acceptProcess(t)
	ctx := t.Context()
	offer := func(socket *daemonSocket, offerID string) daemonprotocol.ToolCallReport {
		t.Helper()
		offer := programToolOffer(f.process.ProcessID, "job", toolcatalog.ToolNameStorageRead,
			`{"path":"/memory/engineering/notes.md"}`)
		offer.OfferID = offerID
		require.NoError(t, socket.handleToolCallOffer(ctx, daemonprotocol.Message{ToolCallOffer: &offer}))
		report := nextToolCallReport(t, socket)
		require.Equal(t, daemonprotocol.ProgramToolCallStateAwaitingApproval, report.State)
		return report
	}
	lateCancel := daemonprotocol.ToolCallCancel{ProcessID: f.process.ProcessID,
		Key: programToolOffer(f.process.ProcessID, "job", "", "").Key, OfferID: "first"}

	first := f.socket()
	attempt := offer(first, "first")
	require.NoError(t, first.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &lateCancel}))
	require.True(t, nextToolCallReport(t, first).CancelRecorded, "this acknowledgment is lost")
	first.toolCallsDone.Wait()

	retrying := f.socket()
	retry := offer(retrying, "retry")
	require.NotEqual(t, attempt.ToolCallID, retry.ToolCallID)

	reconnected := f.socket()
	require.NoError(t, reconnected.handleToolCallCancel(ctx, daemonprotocol.Message{ToolCallCancel: &lateCancel}))
	require.True(t, nextToolCallReport(t, reconnected).CancelRecorded, "nothing of the first attempt is left")
	reconnected.toolCallsDone.Wait()
	state, _ := f.toolCallState(t, retry.ToolCallID)
	require.Equal(t, "awaiting_permission", state, "the late cancel must not end the retry")

	agentPath := "/api/v1/orgs/" + f.project.OrgID + "/projects/" + f.project.ProjectID + "/agents/" +
		testPublicID(t, publicid.KindAgent, f.process.AgentUUID)
	interactions := requestJSONWithHeaders(t, f.handler, http.MethodGet, agentPath+"/interactions", "", "",
		http.StatusOK, authHeaders(f.project.AdminToken))
	listed, ok := interactions["data"].([]any)
	require.True(t, ok)
	var interactionID string
	for _, item := range listed {
		interaction, ok := item.(map[string]any)
		require.True(t, ok)
		if interaction["tool_call_id"] == retry.ToolCallID {
			require.Equal(t, "open", interaction["state"], "the retry's approval is still open")
			interactionID, ok = interaction["id"].(string)
			require.True(t, ok)
		}
	}
	require.NotEmpty(t, interactionID)
	requestJSONWithHeaders(t, f.handler, http.MethodPost, agentPath+"/interactions/"+interactionID+"/resolve",
		httpQuestionResolutionRequest(toolpermission.AllowOptionIndex), "", http.StatusOK,
		authHeaders(f.project.AdminToken))
	done := nextToolCallReport(t, retrying)
	require.Equal(t, daemonprotocol.ProgramToolCallStateCompleted, done.State)
	require.Equal(t, retry.ToolCallID, done.ToolCallID)
	retrying.toolCallsDone.Wait()
}
