//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

const subagentServiceE2ESourceYAML = `instruction: Coordinate helpers and report the outcome.
model:
  provider_config: openai-prod
  name: service-e2e-local
subagents:
  helper:
    type: self
    instruction:
      append: You are the helper subagent.
`

const assistantTextCountSQL = `
SELECT count(*)
FROM agent_events event
JOIN agents agent ON agent.id = event.agent_id
JOIN content_blocks block ON block.agent_id = event.agent_id
  AND block.owner_model_output_id = event.model_output_id
WHERE agent.project_id = $1
  AND event.agent_id = $2
  AND event.event_kind = 'model_output'
  AND block.block_kind = 'text'
  AND block.text_content = $3`

func newSubagentServiceE2EModelServer(
	t *testing.T,
	handleParent func(w http.ResponseWriter, body map[string]any, request int64),
	handleChild func(w http.ResponseWriter, r *http.Request, body map[string]any, request int64),
) (*httptest.Server, *atomic.Int64, *atomic.Int64) {
	var parentRequests, childRequests atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if requestContainsTool(body, "spawn_agent") {
			handleParent(w, body, parentRequests.Add(1))
			return
		}
		handleChild(w, r, body, childRequests.Add(1))
	}))
	return server, &parentRequests, &childRequests
}

var subagentRefPattern = regexp.MustCompile(`"agent_ref":"(agtr-[a-z2-7]+)"`)

func subagentRefFromSpawnResult(output string) string {
	match := subagentRefPattern.FindStringSubmatch(output)
	if match == nil {
		return ""
	}
	return match[1]
}

func failSubagentServiceE2ERequest(t *testing.T) fakeModelFailureFunc {
	return func(w http.ResponseWriter, status int, format string, args ...any) {
		t.Errorf(format, args...)
		http.Error(w, "fake model failure", status)
	}
}

func waitForAssistantText(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectUUID, agentUUID, text string,
) {
	t.Helper()
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		if err := env.db.QueryRow(ctx, assistantTextCountSQL, projectUUID, agentUUID, text).Scan(&count); err != nil {
			return false, err.Error()
		}
		return count == 1, "assistant output not recorded yet"
	})
}

func TestServiceE2EDeterministicSubagentResultArrivesAsMessage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-subagent-result")
	fail := failSubagentServiceE2ERequest(t)
	const childTask = "Summarize why the build failed."
	const childText = "SUBAGENT_RESULT: the build failed because a test timed out"
	const delegatedText = "parent delegated the summary and is waiting to hear back"
	const parentText = "parent relayed the helper's summary"
	openai, parentRequests, childRequests := newSubagentServiceE2EModelServer(
		t,
		func(w http.ResponseWriter, body map[string]any, request int64) {
			switch request {
			case 1:
				writeOpenAIFunctionCall(w, fail, "resp_parent_spawn", "call_spawn", "spawn_agent", map[string]any{
					"agent": "helper",
					"task":  childTask,
					"name":  "summarizer",
				})
			case 2:
				if !requestContainsToolResult(body, "call_spawn", "summarizer") {
					fail(w, http.StatusBadRequest, "second parent request lacks the spawn result: %s", mustJSONString(body))
					return
				}
				writeOpenAIMessage(w, fail, "resp_parent_delegated", delegatedText)
			case 3:
				requestText := mustJSONString(body)
				if !strings.Contains(requestText, "finished its turn") || !strings.Contains(requestText, childText) {
					fail(w, http.StatusBadRequest, "third parent request lacks the subagent result message: %s", requestText)
					return
				}
				writeOpenAIMessage(w, fail, "resp_parent_final", parentText)
			default:
				fail(w, http.StatusTeapot, "unexpected parent request %d: %s", request, mustJSONString(body))
			}
		},
		func(w http.ResponseWriter, _ *http.Request, body map[string]any, request int64) {
			if request != 1 {
				fail(w, http.StatusTeapot, "unexpected child request %d: %s", request, mustJSONString(body))
				return
			}
			requestText := mustJSONString(body)
			if !strings.Contains(requestText, childTask) || !strings.Contains(requestText, "You are the helper subagent.") {
				fail(w, http.StatusBadRequest, "child request lacks its task or appended instruction: %s", requestText)
				return
			}
			if requestContainsTool(body, "read_agent") {
				fail(w, http.StatusBadRequest, "self subagent must not expose subagent tools: %s", requestText)
				return
			}
			writeOpenAIMessage(w, fail, "resp_child_final", childText)
		},
	)
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(t, ctx, "deterministic-subagent-result", subagentServiceE2ESourceYAML)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "delegate the summary to a helper")
	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, delegatedText)
	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, parentText)
	if got := parentRequests.Load(); got != 3 {
		t.Fatalf("parent made %d model requests, want 3", got)
	}
	if got := childRequests.Load(); got != 1 {
		t.Fatalf("child made %d model requests, want 1", got)
	}

	var childName, childKey, childState string
	if err := env.db.QueryRow(
		ctx,
		`SELECT name, subagent_key, state FROM agents WHERE project_id = $1 AND parent_agent_id = $2`,
		projectUUID, agentUUID,
	).Scan(&childName, &childKey, &childState); err != nil {
		t.Fatalf("load spawned subagent: %v", err)
	}
	if childName != "summarizer" || childKey != "helper" || childState != "active" {
		t.Fatalf("subagent = %s/%s/%s, want summarizer/helper/active", childName, childKey, childState)
	}
	listed := env.requestJSON(
		t, ctx, http.MethodGet, project.projectPath+"/agents", nil, "", project.adminToken, http.StatusOK,
	)
	if items, _ := listed["data"].([]any); len(items) != 1 {
		t.Fatalf("top-level agent list returned %d agents, want only the parent: %s", len(items), mustJSONString(listed))
	}
	listedWithChildren := env.requestJSON(
		t,
		ctx,
		http.MethodGet,
		project.projectPath+"/agents?include_subagents=true",
		nil,
		"",
		project.adminToken,
		http.StatusOK,
	)
	if items, _ := listedWithChildren["data"].([]any); len(items) != 2 {
		t.Fatalf("agent list with subagents returned %d agents, want 2: %s", len(items), mustJSONString(listedWithChildren))
	}
}

func TestServiceE2EDeterministicSubagentStopAbortsChild(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-subagent-stop")
	fail := failSubagentServiceE2ERequest(t)
	const parentText = "parent stopped the slow helper"
	childReleased := make(chan struct{})
	childStarted := make(chan struct{})
	var childStartedOnce sync.Once
	var childRequestAborted atomic.Bool
	openai, parentRequests, childRequests := newSubagentServiceE2EModelServer(
		t,
		func(w http.ResponseWriter, body map[string]any, request int64) {
			switch request {
			case 1:
				writeOpenAIFunctionCall(w, fail, "resp_parent_spawn", "call_spawn", "spawn_agent", map[string]any{
					"agent": "helper",
					"task":  "Take as long as you need.",
					"name":  "slow",
				})
			case 2:
				agentRef := subagentRefFromSpawnResult(toolResultOutputForCall(body, "call_spawn"))
				if agentRef == "" {
					fail(w, http.StatusBadRequest, "spawn result lacks an agent_ref: %s", mustJSONString(body))
					return
				}
				select {
				case <-childStarted:
				case <-ctx.Done():
					fail(w, http.StatusServiceUnavailable, "child never started before the parent stopped it")
					return
				}
				writeOpenAIFunctionCall(w, fail, "resp_parent_stop", "call_stop", "stop_agent", map[string]any{
					"agent_ref": agentRef,
				})
			case 3:
				if !requestContainsToolResult(body, "call_stop", "") {
					fail(w, http.StatusBadRequest, "third parent request lacks the stop result: %s", mustJSONString(body))
					return
				}
				writeOpenAIMessage(w, fail, "resp_parent_final", parentText)
			default:
				fail(w, http.StatusTeapot, "unexpected parent request %d: %s", request, mustJSONString(body))
			}
		},
		func(w http.ResponseWriter, r *http.Request, body map[string]any, request int64) {
			if request != 1 {
				fail(w, http.StatusTeapot, "unexpected child request %d: %s", request, mustJSONString(body))
				return
			}
			childStartedOnce.Do(func() { close(childStarted) })
			select {
			case <-r.Context().Done():
				childRequestAborted.Store(true)
			case <-childReleased:
			}
			http.Error(w, "child model call was stopped", http.StatusServiceUnavailable)
		},
	)
	defer openai.Close()
	defer close(childReleased)

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(t, ctx, "deterministic-subagent-stop", subagentServiceE2ESourceYAML)
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "delegate to a helper, then stop it")
	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForAssistantText(t, ctx, env, projectUUID, agentUUID, parentText)
	if got := parentRequests.Load(); got != 3 {
		t.Fatalf("parent made %d model requests, want 3", got)
	}
	if got := childRequests.Load(); got != 1 {
		t.Fatalf("child made %d model requests, want 1", got)
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var childState string
		var locks int
		if err := env.db.QueryRow(
			ctx,
			`SELECT agent.state,
			        (SELECT count(*) FROM agent_runtime_locks runtime_lock WHERE runtime_lock.agent_id = agent.id)
			 FROM agents agent WHERE agent.project_id = $1 AND agent.parent_agent_id = $2`,
			projectUUID, agentUUID,
		).Scan(&childState, &locks); err != nil {
			return false, err.Error()
		}
		if childState != "archived" {
			return false, "subagent state is " + childState
		}
		return locks == 0, "subagent runtime lock still present"
	})
	if !childRequestAborted.Load() {
		t.Fatal("stopping the subagent did not abort its in-flight model call")
	}
}
