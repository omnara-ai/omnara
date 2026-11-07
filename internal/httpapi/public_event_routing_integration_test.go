//go:build integration

package httpapi

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	webhookconfig "github.com/omnara-ai/omnara/internal/eventwebhook/config"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/omnara-ai/omnara/internal/testutil/integrationredis"
	"github.com/stretchr/testify/require"
)

func TestPublicEventRoutingUsesEmittingAgentContextAcrossTransports(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	redisClient := integrationredis.OpenClient(t)
	pool := openIntegrationDB(t, ctx)
	bus, err := notifications.NewRedisBus(redisClient, nil)
	require.NoError(t, err)
	presence, err := notifications.NewRedisPresenceStore(redisClient)
	require.NoError(t, err)
	publisher, err := notifications.NewRoutedPublisher(notifications.RoutedPublisherPorts{
		DaemonWakeups: bus, AgentEventWakeups: bus, ToolCallUpdates: bus, WorkerControls: bus,
	}, presence, nil, nil)
	require.NoError(t, err)
	t.Cleanup(publisher.Close)
	keyWrapper := integrationKeyWrapper()
	store := storage.NewStore(pool, storage.WithSecretKeyWrapper(keyWrapper), storage.WithPostCommitPublisher(publisher))
	server := mustNewServer(t, store,
		WithSecretKeyWrapper(keyWrapper), WithAgentEventWakeupSubscriber(bus),
		WithAgentToolCallUpdateSubscriber(bus), WithAgentStreamDeltaSubscriber(bus),
	)
	handler := newIntegrationHTTPHandler(server.Handler(), pool, store)
	project := bootstrapPublicHTTPProject(t, handler, "event-routing")
	user := createHTTPInteractionUser(t, ctx, pool, store, project.OrgUUID, project.ProjectUUID, "event-routing")
	parent := createHTTPRuntimeAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, user.ID, "routing-parent")
	childProfile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: project.ProjectUUID, Name: "routing-child", CurrentConfigID: parent.AgentConfig.ID,
	})
	require.NoError(t, err)
	actor, err := executionstore.SubagentActorParams(parent.Agent.OrgID, parent.Agent)
	require.NoError(t, err)
	child, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
		ProjectID: project.ProjectUUID, ProfileID: childProfile.ID,
		LaunchedBy:   identitystore.PrincipalRecord{Type: identitystore.PrincipalTypeSystem, ID: parent.Agent.ID},
		MessageActor: actor,
		Subagent: &executionstore.SubagentLaunch{
			ParentAgentID: parent.Agent.ID, Key: "profile-child", MaxDepth: agentconfig.MaxSubagentDepth,
		},
	})
	require.NoError(t, err)
	noProfile := spawnHTTPSubagentForTest(
		t, ctx, store, parent.Agent, parent.AgentConfig.ID, "routing-no-profile", "no-profile-child",
	)
	grandchild := spawnHTTPSubagentForTest(
		t, ctx, store, child.Agent, child.AgentConfig.ID, "routing-grandchild", "grandchild",
	)
	require.Equal(t, parent.Agent.ID, parent.Agent.RootAgentID)
	require.Equal(t, parent.Agent.ID, child.Agent.RootAgentID)
	require.Equal(t, child.Agent.ID, grandchild.ParentAgentID)
	require.Equal(t, parent.Agent.ID, grandchild.RootAgentID)
	require.NotEqual(t, uuid.Nil, parent.Agent.AgentProfileID)
	require.NotEqual(t, parent.Agent.AgentProfileID, child.Agent.AgentProfileID)
	require.Equal(t, uuid.Nil, noProfile.AgentProfileID)
	webhook, err := store.Execution().CreateEventWebhook(ctx, executionstore.CreateEventWebhookInput{
		ProjectID: project.ProjectUUID,
		Scope:     executionstore.EventWebhookScope{Type: executionstore.EventWebhookScopeProject},
		Settings: webhookconfig.Settings{URL: "https://example.com/events", EventTypes: []string{
			"agent_input", "model_output", "tool_result", "tool_call_update",
		}},
	})
	require.NoError(t, err)

	httpServer := httptest.NewServer(handler)
	defer httpServer.Close()
	openStream := func(agentID uuid.UUID) (*http.Response, *bufio.Scanner) {
		t.Helper()
		url := httpServer.URL + project.ProjectPath + "/agents/" +
			testPublicID(t, publicid.KindAgent, agentID) + "/events/stream"
		request, requestErr := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		require.NoError(t, requestErr)
		for key, value := range authHeaders(project.AdminToken) {
			request.Header.Set(key, value)
		}
		response, requestErr := http.DefaultClient.Do(request)
		require.NoError(t, requestErr)
		require.Equal(t, http.StatusOK, response.StatusCode)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		require.True(t, scanner.Scan(), "stream preamble: %v", scanner.Err())
		require.True(t, strings.HasPrefix(scanner.Text(), ":"), "stream preamble: %s", scanner.Text())
		return response, scanner
	}
	assertRouting := func(data map[string]any, agent executionstore.AgentRecord) {
		t.Helper()
		require.Equal(t, project.OrgID, data["org_id"])
		require.Equal(t, project.ProjectID, data["project_id"])
		require.Equal(t, testPublicID(t, publicid.KindAgent, agent.ID), data["agent_id"])
		require.Equal(t, testPublicID(t, publicid.KindAgent, agent.RootAgentID), data["root_agent_id"])
		require.Contains(t, data, "parent_agent_id")
		if agent.ParentAgentID == uuid.Nil {
			require.Nil(t, data["parent_agent_id"])
		} else {
			require.Equal(t, testPublicID(t, publicid.KindAgent, agent.ParentAgentID), data["parent_agent_id"])
		}
		require.Contains(t, data, "agent_profile_id", "an agent without a profile must emit explicit null")
		if agent.AgentProfileID == uuid.Nil {
			require.Nil(t, data["agent_profile_id"])
		} else {
			require.Equal(t, testPublicID(t, publicid.KindAgentProfile, agent.AgentProfileID), data["agent_profile_id"])
		}
	}
	parentStream, parentScanner := openStream(parent.Agent.ID)
	defer parentStream.Body.Close()
	childStream, childScanner := openStream(child.Agent.ID)
	defer childStream.Body.Close()
	agents := []executionstore.AgentRecord{parent.Agent, child.Agent, noProfile, grandchild}
	for _, agent := range agents {
		toolCallID := createHTTPCustomToolCallForAgent(t, ctx, store, project.OrgUUID, project.ProjectUUID, user.ID, agent)
		publicToolCallID := testPublicID(t, publicid.KindToolCall, toolCallID)
		for _, state := range []string{"ready", "completed"} {
			if state == "completed" {
				_, err := store.Execution().CompleteCustomToolCall(ctx, executionstore.CompleteCustomToolCallInput{
					ProjectID: project.ProjectUUID, AgentID: agent.ID, ID: toolCallID,
					Outcome:       executionstore.ToolResultOutcomeSucceeded,
					ContentBlocks: json.RawMessage(`[{"type":"text","text":"done"}]`),
				})
				require.NoError(t, err)
			}
			scanners := []*bufio.Scanner{parentScanner}
			if agent.ID == grandchild.ID {
				scanners = append(scanners, childScanner)
			}
			for _, scanner := range scanners {
				for {
					event, data := nextRoutingEventFrame(t, scanner)
					if event != "tool_call_update" || data["tool_call_id"] != publicToolCallID || data["state"] != state {
						continue
					}
					assertRouting(data, agent)
					break
				}
			}
		}
	}

	for _, agent := range agents {
		publicAgentID := testPublicID(t, publicid.KindAgent, agent.ID)
		listed := requestJSONWithHeaders(t, handler, http.MethodGet,
			project.ProjectPath+"/agents/"+publicAgentID+"/events", "", "", http.StatusOK, authHeaders(project.AdminToken))
		events := testutil.RequireType[[]any](t, listed["data"])
		require.NotEmpty(t, events)
		kinds := map[string]bool{}
		endpoint, err := store.Execution().GetEventWebhookEndpoint(ctx, project.OrgUUID, webhook.ID, agent.ID)
		require.NoError(t, err)
		require.Equal(t, project.OrgUUID, endpoint.OrgID)
		require.Equal(t, project.ProjectUUID, endpoint.ProjectID)
		require.Equal(t, agent.AgentProfileID, endpoint.AgentProfileID,
			"a project webhook must use its emitting agent's profile, not its subscription's null profile")
		require.Equal(t, agent.ParentAgentID, endpoint.ParentAgentID)
		require.Equal(t, agent.RootAgentID, endpoint.RootAgentID)
		response, scanner := openStream(agent.ID)
		for _, raw := range events {
			event := testutil.RequireType[map[string]any](t, raw)
			assertRouting(event, agent)
			kind := testutil.RequireType[string](t, event["event_kind"])
			kinds[kind] = true
			streamKind, streamEvent := nextRoutingEventFrame(t, scanner)
			require.Equal(t, kind, streamKind)
			require.Equal(t, event, streamEvent, "REST and durable SSE must expose the same routing fields")
			webhookEvent, err := store.Execution().GetAgentEventForWebhook(
				ctx, project.ProjectUUID, agent.ID, jsonInt64(t, event["sequence"]),
			)
			require.NoError(t, err)
			require.Equal(t, agent.AgentProfileID, webhookEvent.AgentProfileID)
			require.Equal(t, agent.ParentAgentID, webhookEvent.ParentAgentID)
			require.Equal(t, agent.RootAgentID, webhookEvent.RootAgentID)
		}
		require.NoError(t, response.Body.Close())
		for _, kind := range []string{"agent_input", "model_output", "tool_result"} {
			require.True(t, kinds[kind], "missing %s coverage for agent %s", kind, publicAgentID)
		}
	}
}

func nextRoutingEventFrame(t *testing.T, scanner *bufio.Scanner) (string, map[string]any) {
	t.Helper()
	var event string
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			var data map[string]any
			require.NoError(t, json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data))
			require.NotEqual(t, "error", event, "stream error: %+v", data)
			return event, data
		}
	}
	t.Fatalf("event stream ended before the expected frame: %v", scanner.Err())
	return "", nil
}
