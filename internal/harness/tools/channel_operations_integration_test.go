//go:build integration

package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

type testChannelOperations func(
	context.Context, channelconnector.OperationRequest,
) (channelconnector.OperationResult, error)

func (f testChannelOperations) Execute(
	ctx context.Context, request channelconnector.OperationRequest,
) (channelconnector.OperationResult, error) {
	return f(ctx, request)
}

func managedToolTurn(fixture integrationToolFixture) Turn {
	turn := fixture.turn()
	for _, name := range []string{
		toolcatalog.ToolNameSendChannelMessage, toolcatalog.ToolNameReadChannel, toolcatalog.ToolNameListChannels,
	} {
		turn.Tools[name] = ToolSpec{Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)}
	}
	return turn
}

func completedTestChannelSend(request channelconnector.OperationRequest) channelconnector.OperationResult {
	return channelconnector.OperationResult{RequestID: request.RequestID, Outcome: channelconnector.OperationCompleted,
		Payload: json.RawMessage(`{"publication":"published","message_channel":"destination","message_id":"sent-1"}`)}
}

func TestManagedChannelSendUsesExactRouteAndReplaysRecordedResult(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "managed-route")
	channel := createConnectorToolChannel(t, ctx, f, "managed-route")
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	raw := `{"channel_id":"` + id + `","message":{"text":" hello connector "},"params":{"number":9007199254740993}}`
	calls := []model.ToolCall{
		{ID: "send", Name: toolcatalog.ToolNameSendChannelMessage, Input: json.RawMessage(raw)},
		{ID: "implicit", Name: toolcatalog.ToolNameSendChannelMessage, Input: json.RawMessage(`{"message":{"text":"no fallback"}}`)},
		{ID: "list", Name: toolcatalog.ToolNameListChannels, Input: json.RawMessage(`{}`)},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	turn := managedToolTurn(f)
	var observed channelconnector.OperationRequest
	attempts := 0
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		_ context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		attempts++
		observed = request
		return completedTestChannelSend(request), nil
	})}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, calls[0])
	require.NoError(t, err)
	require.Equal(t, DispatchCompleted, result.Disposition)
	require.Equal(t, 1, attempts)
	require.Equal(t, channelconnector.Capability{
		ConnectorKey: channel.App.ConnectorKey, Provider: channel.App.Provider,
	}, observed.Capability)
	require.Equal(t, id, observed.Scope.ChannelID)
	var payload channelconnector.SendPayload
	require.NoError(t, json.Unmarshal(observed.Payload, &payload))
	require.Equal(t, channel.Target.ProviderRef, payload.Destination.ProviderRef)
	require.Equal(t, " hello connector ", payload.Message.Text)
	require.JSONEq(t, `{"number":9007199254740993}`, string(payload.Params))
	body := toolResultMapFromTestParts(t, result.ContentParts)
	require.NotContains(t, body, "status")
	message, ok := body["message"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, id, message["channel_id"])
	require.Equal(t, "sent-1", message["message_id"])
	replay, err := executor.Dispatch(ctx, turn, calls[0])
	require.NoError(t, err)
	require.JSONEq(t, string(result.ContentParts), string(replay.ContentParts))
	require.Equal(t, 1, attempts, "recorded tool replay must not repeat the provider mutation")
	record, err := f.Store.Execution().GetToolCall(ctx, turn.ProjectID, turn.AgentID, f.toolCallID(t, ctx, calls[0].ID))
	require.NoError(t, err)
	require.JSONEq(t, raw, string(record.Input), "dispatch must not rewrite original arguments")
	implicit, err := executor.Dispatch(ctx, turn, calls[1])
	require.NoError(t, err)
	require.Equal(t, "malformed", toolResultMapFromTestParts(t, implicit.ContentParts)["error_code"])
	require.Equal(t, 1, attempts)
	listed, err := executor.Dispatch(ctx, turn, calls[2])
	require.NoError(t, err)
	require.Contains(t, string(listed.ContentParts), id)
}

func TestManagedChannelSendOnlyGrantDoesNotRequireReceiveOrRead(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "managed-sendonly")
	channel := createConnectorToolChannel(t, ctx, f, "managed-sendonly")
	_, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, IntegrationInstallID: channel.Install.ID,
			IntegrationTargetID: channel.Target.ID, IntegrationRouteID: channel.Route.ID,
			SendAllowed: true, Source: "test", Metadata: json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "send", toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+id+`","message":{"text":"hello"}}`, f.Now)
	attempts := 0
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		_ context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		attempts++
		return completedTestChannelSend(request), nil
	})}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, managedToolTurn(f), call)
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
	require.Contains(t, toolResultMapFromTestParts(t, result.ContentParts), "message")
	access, err := f.Store.Integrations().GetAgentChannelAccess(ctx, toolsTestProjectID, f.Agent.ID, channel.Target.ID)
	require.NoError(t, err)
	require.False(t, access.Capabilities.Read)
	require.False(t, access.ReceiveAllowed)
}

func TestManagedChannelSendUnknownOutcomeIsTerminalWithoutRetry(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "managed-unknown")
	channel := createConnectorToolChannel(t, ctx, f, "managed-unknown")
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "send", toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+id+`","message":{"text":"hello"}}`, f.Now)
	attempts := 0
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		context.Context, channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		attempts++
		return channelconnector.OperationResult{}, errors.New("response lost after acceptance; secret=do-not-expose")
	})}
	turn := managedToolTurn(f)
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
	require.NoError(t, err)
	body := toolResultMapFromTestParts(t, result.ContentParts)
	require.Equal(t, "unknown", body["status"])
	require.NotContains(t, string(result.ContentParts), "do-not-expose")
	_, err = executor.Dispatch(ctx, turn, call)
	require.NoError(t, err)
	require.Equal(t, 1, attempts)
}

func TestManagedChannelSendCancellationAbortsIOAndRecordsUnknown(t *testing.T) {
	f := newIntegrationToolFixture(t, context.Background(), "managed-cancel")
	channel := createConnectorToolChannel(t, context.Background(), f, "managed-cancel")
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, context.Background(), "send", toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+id+`","message":{"text":"hello"}}`, f.Now)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	started := make(chan time.Time, 1)
	stopped := make(chan struct{})
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		ioCtx context.Context, _ channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		actualDeadline, _ := ioCtx.Deadline()
		started <- actualDeadline
		<-ioCtx.Done()
		close(stopped)
		return channelconnector.OperationResult{}, ioCtx.Err()
	})}
	turn := managedToolTurn(f)
	scope := NewAsyncExecutionScope(nil)
	result, err := executor.Dispatch(WithAsyncExecutionScope(ctx, scope), turn, call)
	require.NoError(t, err)
	require.Equal(t, DispatchDeferred, result.Disposition)
	scope.Seal()
	select {
	case actual := <-started:
		require.Equal(t, deadline, actual)
	case <-ctx.Done():
		t.Fatal("operation did not start")
	}
	cancel()
	select {
	case <-scope.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("canceled operation did not finish")
	}
	require.NoError(t, scope.Err())
	select {
	case <-stopped:
	default:
		t.Fatal("operation I/O remained active after cancellation")
	}
	result, err = executor.Dispatch(context.Background(), turn, call)
	require.NoError(t, err)
	require.Equal(t, "unknown", toolResultMapFromTestParts(t, result.ContentParts)["status"])
}

func TestManagedChannelReadMapsProviderFactsAndScopesPagination(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "managed-read")
	channel := createConnectorToolChannel(t, ctx, f, "managed-read")
	_, err := f.Store.Integrations().CreateIntegrationTargetBinding(ctx,
		integrationstore.CreateIntegrationTargetBindingInput{
			ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, IntegrationInstallID: channel.Install.ID,
			IntegrationTargetID: channel.Target.ID, IntegrationRouteID: channel.Route.ID,
			ReadAllowed: true, Source: "test", Metadata: json.RawMessage(`{}`),
		})
	require.NoError(t, err)
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "read", toolcatalog.ToolNameReadChannel, `{"channel_id":"`+id+`","limit":2}`, f.Now)
	var observed channelconnector.ReadPayload
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		_ context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		if err := json.Unmarshal(request.Payload, &observed); err != nil {
			return channelconnector.OperationResult{}, err
		}
		return channelconnector.OperationResult{RequestID: request.RequestID, Outcome: channelconnector.OperationCompleted,
			Payload: json.RawMessage(`{"messages":[{"content":{"text":"retained"},"publication":"published","message_id":"one","reply_to":{"message_id":"zero"},"reply_channel":{"implementation_key":"test-channel","provider_ref":"unregistered-child","provider_ref_kind":"thread"}}],"coverage":"partial","coverage_reason":"provider omitted older content","next_cursor":"provider opaque/next"}`)}, nil
	})}
	turn := managedToolTurn(f)
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, turn, call)
	require.NoError(t, err)
	require.Equal(t, 2, observed.Limit)
	require.Empty(t, observed.Cursor)
	body := toolResultMapFromTestParts(t, result.ContentParts)
	require.NotContains(t, body, "status")
	require.Equal(t, "partial", body["coverage"])
	require.NotContains(t, body, "request_id", "successful history pages use the canonical ChannelHistoryPage schema")
	messages, ok := body["messages"].([]any)
	require.True(t, ok, "result: %s", result.ContentParts)
	require.Len(t, messages, 1)
	message, ok := messages[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, id, message["channel_id"])
	require.NotContains(t, message, "reply_channel_id", "reading cannot create an unregistered child")
	reply, ok := message["reply_to"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, id, reply["channel_id"])
	cursor, ok := body["next_cursor"].(string)
	require.True(t, ok)
	providerCursor, err := decodeChannelHistoryCursor(cursor, turn, channel.Target.ID)
	require.NoError(t, err)
	require.Equal(t, "provider opaque/next", providerCursor)
	_, err = decodeChannelHistoryCursor(cursor, turn, f.Target.ID)
	require.Error(t, err)
	_, err = f.Store.Integrations().GetIntegrationTargetByProviderRef(
		ctx, toolsTestProjectID, channel.Install.ID, "unregistered-child")
	require.Error(t, err)
}

// This fixture only accepts writes. Any blob access before artifact authorization
// would fail the test; no S3 server, remote credentials or buffered egress is used.
type channelArtifactWriteFixture struct{ blobstore.Store }

func (*channelArtifactWriteFixture) PutBlob(_ context.Context, _ string, content []byte) (blobstore.Metadata, error) {
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func TestManagedChannelSendRejectsForeignArtifactBeforeProviderIO(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixtureWithMCP(t, ctx, "managed-foreign-file", false,
		storage.WithBlobStore(&channelArtifactWriteFixture{}))
	channel := createConnectorToolChannel(t, ctx, f, "managed-foreign-file")
	_, err := f.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: channel.Install.ID,
			ImplementationKey: "test-channel", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object"}`),
			Capabilities:     integrationstore.ChannelCapabilities{Send: true, Text: true, Artifacts: true},
			ConnectorCapabilities: []channelconnector.Capability{{
				ConnectorKey: channel.App.ConnectorKey, Provider: channel.App.Provider,
			}},
		})
	require.NoError(t, err)
	other, err := f.Store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: toolsTestProjectID, ProfileID: f.Profile.ID, AgentConfigID: f.Profile.CurrentConfigID,
		LaunchedBy: toolsTestUserPrincipal(f.User.ID), IdempotencyKey: "foreign-artifact-owner",
	})
	require.NoError(t, err)
	artifact, err := f.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: toolsTestProjectID, AgentID: other.Agent.ID, ContentType: "text/plain",
		Filename: "private.txt", Content: []byte("private content"), MaxBytes: 1024, IdempotencyKey: "foreign-artifact",
	})
	require.NoError(t, err)
	artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	require.NoError(t, err)
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "send", toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+id+`","message":{"artifact_ids":["`+artifactID+`"]}}`, f.Now)
	attempts := 0
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		context.Context, channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		attempts++
		return channelconnector.OperationResult{}, errors.New("unexpected provider I/O")
	})}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, managedToolTurn(f), call)
	require.NoError(t, err)
	require.Zero(t, attempts)
	require.Equal(t, "artifact_unavailable", toolResultMapFromTestParts(t, result.ContentParts)["code"])
}

func TestManagedChannelSendChecksCurrentParamsBeforeProviderIO(t *testing.T) {
	ctx := context.Background()
	f := newIntegrationToolFixture(t, ctx, "managed-params")
	channel := createConnectorToolChannel(t, ctx, f, "managed-params")
	id, err := publicid.Encode(publicid.KindIntegrationTarget, channel.Target.ID)
	require.NoError(t, err)
	call := f.recordToolCall(t, ctx, "send", toolcatalog.ToolNameSendChannelMessage,
		`{"channel_id":"`+id+`","message":{"text":"hello"},"params":{"stale_option":true}}`, f.Now)
	// Change the current schema after model output; execution must not use an old
	// declaration or rewrite these original arguments to satisfy the new schema.
	_, err = f.Store.Integrations().PublishConnectorChannelDefinition(ctx,
		integrationstore.PublishChannelDefinitionInput{
			ProjectID: toolsTestProjectID, IntegrationInstallID: channel.Install.ID,
			ImplementationKey: "test-channel", Kind: integrationstore.ChannelKindExternal,
			SendParamsSchema: json.RawMessage(`{"type":"object","additionalProperties":false}`),
			Capabilities:     integrationstore.ChannelCapabilities{Send: true, Text: true},
			ConnectorCapabilities: []channelconnector.Capability{{
				ConnectorKey: channel.App.ConnectorKey, Provider: channel.App.Provider,
			}},
		})
	require.NoError(t, err)
	attempts := 0
	executor := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		context.Context, channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		attempts++
		return channelconnector.OperationResult{}, errors.New("unexpected provider I/O")
	})}
	result, err := dispatchAsyncToolToTerminal(t, ctx, executor, managedToolTurn(f), call)
	require.NoError(t, err)
	require.Zero(t, attempts)
	require.Equal(t, "invalid_channel_request", toolResultMapFromTestParts(t, result.ContentParts)["code"])
}

func TestManagedChannelSendKeepsDistinctCallsAndStreamsAuthorizedArtifact(t *testing.T) {
	ctx := context.Background()
	blobs := &channelArtifactStreamFixture{content: []byte("artifact content")}
	f := newIntegrationToolFixtureWithMCP(t, ctx, "managed-artifact", false, storage.WithBlobStore(blobs))
	artifact, err := f.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: toolsTestProjectID, AgentID: f.Agent.ID, ContentType: "text/plain",
		Filename: "../artifact.txt", Content: blobs.content, MaxBytes: 1024, IdempotencyKey: "send-artifact",
	})
	require.NoError(t, err)
	artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	require.NoError(t, err)
	channelID, err := publicid.Encode(publicid.KindIntegrationTarget, f.Target.ID)
	require.NoError(t, err)
	raw := json.RawMessage(`{"channel_id":"` + channelID + `","message":{"text":"caption","artifact_ids":["` + artifactID + `"]}}`)
	calls := []model.ToolCall{
		{ID: "first", Name: toolcatalog.ToolNameSendChannelMessage, Input: raw},
		{ID: "second", Name: toolcatalog.ToolNameSendChannelMessage, Input: raw},
	}
	f.recordToolCalls(t, ctx, calls, f.Now)
	var ids []string
	e := Executor{Store: f.Store, ChannelOperations: testChannelOperations(func(
		ioCtx context.Context, request channelconnector.OperationRequest,
	) (channelconnector.OperationResult, error) {
		require.Equal(t, len(ids), blobs.opens, "artifact source stays unopened until transport requests it")
		ids = append(ids, request.RequestID)
		require.Len(t, request.Artifacts, 1)
		file := request.Artifacts[0]
		require.Equal(t, artifactID, file.ID)
		require.Equal(t, "artifact.txt", file.Filename)
		reader, err := file.Open(ioCtx)
		if err != nil {
			return channelconnector.OperationResult{}, err
		}
		content, readErr := io.ReadAll(reader)
		closeErr := reader.Close()
		require.Equal(t, blobs.content, content)
		if err := errors.Join(readErr, closeErr); err != nil {
			return channelconnector.OperationResult{}, err
		}
		return completedTestChannelSend(request), nil
	})}
	for _, call := range calls {
		result, err := dispatchAsyncToolToTerminal(t, ctx, e, managedToolTurn(f), call)
		require.NoError(t, err)
		require.Equal(t, DispatchCompleted, result.Disposition)
		body := toolResultMapFromTestParts(t, result.ContentParts)
		require.Contains(t, body, "message")
	}
	require.Len(t, ids, 2)
	require.NotEqual(t, ids[0], ids[1], "separate calls are not collapsed by identical content")
	require.Equal(t, 2, blobs.opens)
	require.Equal(t, 2, blobs.closes)
}

type channelArtifactStreamFixture struct {
	channelArtifactWriteFixture
	content       []byte
	opens, closes int
}

func (s *channelArtifactStreamFixture) OpenBlob(context.Context, string) (io.ReadCloser, blobstore.Metadata, error) {
	s.opens++
	return &channelArtifactTestReader{Reader: bytes.NewReader(s.content), onClose: func() { s.closes++ }},
		blobstore.Metadata{Digest: blobstore.ContentDigest(s.content), SizeBytes: int64(len(s.content))}, nil
}

type channelArtifactTestReader struct {
	io.Reader
	onClose func()
}

func (r *channelArtifactTestReader) Close() error { r.onClose(); return nil }
