//go:build integration && servicee2e

package e2e

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
)

type serviceAgentChange struct {
	AgentID       string   `json:"agent_id"`
	ParentAgentID *string  `json:"parent_agent_id"`
	Changes       []string `json:"changes"`
}

func TestServiceE2EIdleParentReceivesChildInteractionChanges(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "idle-parent-child-changes")
	fail := failSubagentServiceE2ERequest(t)
	askAfterParentIdle := make(chan struct{})
	const delegated = "The child is working independently."
	const childFinished = "The answer was received."
	model, parentRequests, childRequests := newSubagentServiceE2EModelServer(t,
		func(w http.ResponseWriter, body map[string]any, request int64) {
			if request == 1 {
				writeOpenAIFunctionCall(w, fail, "parent_spawn", "spawn_child", "spawn_agent", map[string]any{
					"agent": "helper", "task": "Ask whether to continue.", "name": "reviewer",
				})
				return
			}
			writeOpenAIMessage(w, fail, "parent_ack", delegated)
		},
		func(w http.ResponseWriter, r *http.Request, body map[string]any, request int64) {
			if request == 1 {
				select {
				case <-askAfterParentIdle:
				case <-r.Context().Done():
					return
				}
				writeOpenAIFunctionCall(w, fail, "child_question", "child_question", "ask_question", map[string]any{
					"questions": []map[string]any{{"prompt": "Continue the review?", "options": []map[string]any{{"label": "Yes"}}}},
				})
				return
			}
			if !requestContainsToolResult(body, "child_question", "Yes") {
				fail(w, http.StatusBadRequest, "child continuation lacks the answer: %s", mustJSONString(body))
				return
			}
			writeOpenAIMessage(w, fail, "child_answered", childFinished)
		},
	)
	defer func() {
		cancel()
		model.Close()
	}()
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithSource(
		t, ctx, "idle-parent-child-changes", subagentServiceE2ESourceYAML+"\ntools:\n  ask_question: {}\n",
	)
	parentID := project.createAgent(t, ctx)

	request, err := env.newAPIRequest(ctx, http.MethodGet, project.projectPath+"/agents/"+parentID+"/events/stream", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+project.adminToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("stream status = %d", response.StatusCode)
	}
	changes := make(chan serviceAgentChange, 64)
	go func() {
		defer close(changes)
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 1024*1024)
		var event string
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			} else if event == "agent_change" && strings.HasPrefix(line, "data: ") {
				var change serviceAgentChange
				if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &change); err != nil {
					t.Errorf("decode agent_change: %v", err)
					return
				}
				select {
				case changes <- change:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	waitChange := func(owner, kind string) serviceAgentChange {
		t.Helper()
		for {
			select {
			case change, open := <-changes:
				if !open {
					t.Fatal("root stream closed before the child notification")
				}
				if change.AgentID != parentID && (owner == "" || owner == change.AgentID) && slices.Contains(change.Changes, kind) {
					if change.ParentAgentID == nil || *change.ParentAgentID != parentID {
						t.Fatalf("child change has wrong parent: %+v", change)
					}
					return change
				}
			case <-ctx.Done():
				t.Fatalf("waiting for %s change from %s: %v", kind, owner, ctx.Err())
			}
		}
	}

	project.createInput(t, ctx, parentID, "Delegate the question.")
	env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL})
	created := waitChange("", "agent")
	childID := created.AgentID
	children := env.requestJSON(t, ctx, http.MethodGet,
		project.projectPath+"/agents?parent_agent_id="+parentID, nil, "", project.adminToken, http.StatusOK)
	rows := testutil.RequireType[[]any](t, children["data"])
	if len(rows) != 1 || testutil.RequireType[map[string]any](t, rows[0])["id"] != childID {
		t.Fatalf("children after change = %s", mustJSONString(children))
	}
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	parentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, parentID)
	waitForAssistantText(t, ctx, env, projectUUID, parentUUID, delegated)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var active int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_runtime_locks WHERE agent_id = $1`, parentUUID).Scan(&active)
		return err == nil && active == 0, "parent runtime has not gone idle"
	})
	close(askAfterParentIdle)

	waitChange(childID, "interactions")
	interactionPath := project.projectPath + "/agents/" + parentID + "/interactions?state=open&include_subagents=true"
	listed := env.requestJSON(t, ctx, http.MethodGet, interactionPath, nil, "", project.adminToken, http.StatusOK)
	items := testutil.RequireType[[]any](t, listed["data"])
	if len(items) != 1 {
		t.Fatalf("open child requests after notification = %s", mustJSONString(listed))
	}
	interaction := testutil.RequireType[map[string]any](t, items[0])
	if interaction["agent_id"] != childID || interaction["interaction_kind"] != "question" {
		t.Fatalf("request owner/kind = %s", mustJSONString(interaction))
	}
	interactionID := testutil.RequireType[string](t, interaction["id"])
	env.requestJSON(t, ctx, http.MethodPost,
		project.projectPath+"/agents/"+childID+"/interactions/"+interactionID+"/resolve",
		map[string]any{"answers": []map[string]any{{"option_indices": []int{0}}}}, "", project.adminToken, http.StatusOK)
	waitChange(childID, "interactions")
	listed = env.requestJSON(t, ctx, http.MethodGet, interactionPath, nil, "", project.adminToken, http.StatusOK)
	if items := testutil.RequireType[[]any](t, listed["data"]); len(items) != 0 {
		t.Fatalf("resolved child request remains open: %s", mustJSONString(listed))
	}
	childUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, childID)
	defer func() {
		if !t.Failed() {
			return
		}
		t.Logf("model requests parent=%d child=%d", parentRequests.Load(), childRequests.Load())
	}()
	waitForAssistantText(t, ctx, env, projectUUID, childUUID, childFinished)
}
