//go:build blackbox

package blackbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

// Test 1: the happy path. Creating an agent from the suite profile and config
// returns the full launch state, and the agent is immediately readable.
func TestCreateAgentHappyPath(t *testing.T) {
	launched := createAgentForTest(t, "create agent", map[string]any{
		"profile": fx.profileID,
		"config":  fx.configID,
	}, uniqueKey(t, "create"))

	agentID := getString(t, launched, "agent.id")
	if !strings.HasPrefix(agentID, "agt_") {
		t.Errorf("agent id %q does not have the agt_ public-id prefix", agentID)
	}
	if state := getString(t, launched, "agent.state"); state != "active" {
		t.Errorf("new agent state = %q, want active", state)
	}
	if orgID := getString(t, launched, "agent.org_id"); orgID != fx.orgID {
		t.Errorf("agent org_id = %q, want %q", orgID, fx.orgID)
	}
	if projectID := getString(t, launched, "agent.project_id"); projectID != fx.projectID {
		t.Errorf("agent project_id = %q, want %q", projectID, fx.projectID)
	}
	if configID := getString(t, launched, "agent_config.id"); configID != fx.configID {
		t.Errorf("agent_config.id = %q, want launch config %q", configID, fx.configID)
	}
	if _, ok := launched["machine_bindings"].([]any); !ok {
		t.Errorf("machine_bindings missing or not an array: %v", launched["machine_bindings"])
	}

	fetched := api(t, "read agent back",
		http.MethodGet, fx.projectPath+"/agents/"+agentID, nil).
		requireStatus(t, http.StatusOK).json(t)
	if fetchedID := getString(t, fetched, "agent.id"); fetchedID != agentID {
		t.Errorf("GET returned agent id %q, want %q", fetchedID, agentID)
	}
	if state := getString(t, fetched, "agent.state"); state != "active" {
		t.Errorf("GET agent state = %q, want active", state)
	}
}

// Test 2: creating an agent with an initial message queues that message as
// the agent's first input.
func TestCreateAgentWithInitialMessage(t *testing.T) {
	const message = "Blackbox suite initial message. Do not act on this."
	launched := createAgentForTest(t, "create agent with initial message",
		map[string]any{
			"profile": fx.profileID,
			"config":  fx.configID,
			"message": message,
		}, uniqueKey(t, "create"))

	agentInput, ok := launched["agent_input"].(map[string]any)
	if !ok {
		t.Fatalf("launch response has no agent_input object: %v", launched["agent_input"])
	}
	if inputID := getString(t, agentInput, "id"); !strings.HasPrefix(inputID, "ain_") {
		t.Errorf("agent_input.id %q does not have the ain_ public-id prefix", inputID)
	}
	if inputAgentID := getString(t, agentInput, "agent_id"); inputAgentID != getString(t, launched, "agent.id") {
		t.Errorf("agent_input.agent_id = %q, want %q", inputAgentID, getString(t, launched, "agent.id"))
	}
}

// Test 3: replaying the same create request with the same idempotency key
// returns 200 (not 201) with the same agent, and no duplicate is created.
func TestCreateAgentIdempotentReplay(t *testing.T) {
	key := uniqueKey(t, "replay")
	body := map[string]any{"profile": fx.profileID, "config": fx.configID}

	launched := createAgentForTest(t, "create agent", body, key)
	agentID := getString(t, launched, "agent.id")

	replayed := apiIdem(t, "repeat identical request (replay)",
		http.MethodPost, fx.projectPath+"/agents", body, key).
		requireStatus(t, http.StatusOK).json(t)
	if replayedID := getString(t, replayed, "agent.id"); replayedID != agentID {
		t.Errorf("replay returned agent %q, want original agent %q", replayedID, agentID)
	}
	if len(replayed) != 1 {
		t.Errorf("replay returned launch-only fields: %v", replayed)
	}
}

// Test 4: the first launch associated with an idempotency key wins. Reusing
// that key returns the current agent without applying a changed body.
func TestCreateAgentIdempotencyKeyFirstWriteWins(t *testing.T) {
	key := uniqueKey(t, "conflict")
	launched := createAgentForTest(t, "create agent",
		map[string]any{"profile": fx.profileID, "config": fx.configID}, key)

	conflicting := map[string]any{
		"profile": fx.profileID,
		"config":  fx.configID,
		"message": "different body under the same idempotency key",
	}
	replayed := apiIdem(t, "reuse key with different body",
		http.MethodPost, fx.projectPath+"/agents", conflicting, key).
		requireStatus(t, http.StatusOK).json(t)
	if got, want := getString(t, replayed, "agent.id"), getString(t, launched, "agent.id"); got != want {
		t.Errorf("changed replay returned agent %q, want original agent %q", got, want)
	}
	if len(replayed) != 1 {
		t.Errorf("changed replay returned launch-only fields: %v", replayed)
	}
}

// Test 5: malformed create requests are rejected with 400 and a structured
// error body, before any agent is created. These are deployment spot checks
// only; the exhaustive validation matrix (field types, ID shapes, every
// route) is covered in-repo by the internal/httpapi strict-validation and
// OpenAPI contract tests.
func TestCreateAgentValidation(t *testing.T) {
	cases := []struct {
		name string
		body any
	}{
		{"missing config", map[string]any{"profile": fx.profileID}},
		{"malformed json", `{"config": `},
		{"unknown field", map[string]any{"config": fx.configID, "bogus_field": true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res := apiIdem(t, "create with "+tc.name,
				http.MethodPost, fx.projectPath+"/agents", tc.body, uniqueKey(t, "validation"))
			res.requireStatus(t, http.StatusBadRequest)
			res.errorMessage(t)
		})
	}
}

// Test 6: a well-formed config ID that does not exist yields 404, not a
// server error or a silently misconfigured agent.
func TestCreateAgentUnknownConfig(t *testing.T) {
	// Flip one character in the middle of the real config ID. The result is
	// still a valid public ID (so it passes request validation) but refers to
	// a config that does not exist.
	missingConfigID := flipPublicIDChar(t, fx.configID)
	apiIdem(t, "create with nonexistent config id",
		http.MethodPost, fx.projectPath+"/agents",
		map[string]any{"config": missingConfigID}, uniqueKey(t, "missing")).
		requireStatus(t, http.StatusNotFound).errorMessage(t)
}

// Test 7: agent creation requires a valid bearer token.
func TestCreateAgentAuthentication(t *testing.T) {
	body := map[string]any{"profile": fx.profileID, "config": fx.configID}

	t.Run("no token", func(t *testing.T) {
		apiWith(t, http.MethodPost, fx.projectPath+"/agents", body,
			requestOptions{useAuth: false, note: "create without auth header"}).
			requireStatus(t, http.StatusUnauthorized).errorMessage(t)
	})
	t.Run("invalid token", func(t *testing.T) {
		apiWith(t, http.MethodPost, fx.projectPath+"/agents", body,
			requestOptions{
				useAuth:       true,
				tokenOverride: "not-an-omnara-token",
				note:          "create with invalid token",
			}).
			requireStatus(t, http.StatusUnauthorized).errorMessage(t)
	})
}

// Test 8: a config from one project cannot be used to launch an agent in a
// different project, even within the same org.
func TestCreateAgentProjectScoping(t *testing.T) {
	otherProject := apiIdem(t, "create second project",
		http.MethodPost, fx.orgPath+"/projects",
		map[string]any{"name": "Blackbox scoping " + fx.runID},
		uniqueKey(t, "project")).
		requireStatus(t, http.StatusCreated).json(t)
	otherProjectPath := fx.orgPath + "/projects/" + getString(t, otherProject, "id")

	apiIdem(t, "create agent there with other project's config",
		http.MethodPost, otherProjectPath+"/agents",
		map[string]any{"config": fx.configID}, uniqueKey(t, "cross")).
		requireStatus(t, http.StatusNotFound).errorMessage(t)
}

// Test 9: the create/archive lifecycle. Archiving returns the agent, keeps it
// readable, and repeat archives are idempotent no-ops.
func TestCreateAgentArchiveLifecycle(t *testing.T) {
	launched := createAgentForTest(t, "create agent", map[string]any{
		"profile": fx.profileID,
		"config":  fx.configID,
	}, uniqueKey(t, "create"))
	agentID := getString(t, launched, "agent.id")
	agentPath := fx.projectPath + "/agents/" + agentID

	archived := api(t, "archive agent",
		http.MethodPost, agentPath+"/archive", nil).
		requireStatus(t, http.StatusOK).json(t)
	if getString(t, archived, "agent.state") != "archived" {
		t.Errorf("archive response = %v, want state archived", archived)
	}

	read := api(t, "read archived agent",
		http.MethodGet, agentPath, nil).
		requireStatus(t, http.StatusOK).json(t)
	if getString(t, read, "agent.state") != "archived" {
		t.Errorf("archived agent read = %v, want state archived", read)
	}

	rearchived := api(t, "archive again (idempotent)",
		http.MethodPost, agentPath+"/archive", nil).
		requireStatus(t, http.StatusOK).json(t)
	if getString(t, rearchived, "agent.state") != "archived" {
		t.Errorf("repeat archive response = %v, want state archived", rearchived)
	}

	// A well-formed agent ID that never existed still yields 404.
	api(t, "archive nonexistent agent id",
		http.MethodPost, fx.projectPath+"/agents/"+flipPublicIDChar(t, agentID)+"/archive", nil).
		requireStatus(t, http.StatusNotFound).errorMessage(t)
}

// flipPublicIDChar swaps one base32 character in the middle of a public ID so
// the result stays well-formed but points at a nonexistent resource.
func flipPublicIDChar(t *testing.T, id string) string {
	t.Helper()
	prefix, token, ok := strings.Cut(id, "_")
	if !ok || len(token) < 10 {
		t.Fatalf("unexpected public id shape: %q", id)
	}
	chars := []byte(token)
	// Position 10 is mid-token: far from the base32 tail, so flipping it keeps
	// the decoded payload 16 bytes and non-nil.
	if chars[10] == 'a' {
		chars[10] = 'b'
	} else {
		chars[10] = 'a'
	}
	return prefix + "_" + string(chars)
}

// Test 10: concurrent creates with distinct idempotency keys all succeed and
// produce distinct agents that show up in the project listing.
func TestCreateAgentConcurrent(t *testing.T) {
	const workers = 8
	step(t, "launch %d parallel creates", workers)

	// Requests run on worker goroutines, so they collect errors instead of
	// failing the test directly, and all testing.T interaction (uniqueKey
	// calls t.Helper) happens on the test goroutine before workers start.
	type outcome struct {
		agentID string
		err     error
	}
	keys := make([]string, workers)
	scopes := make([]string, workers)
	for i := range workers {
		keys[i] = uniqueKey(t, fmt.Sprintf("worker-%d", i))
		scopes[i] = fmt.Sprintf("%s/worker-%d", t.Name(), i)
	}
	results := make([]outcome, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			res, err := fx.client.do(ctx, http.MethodPost, fx.projectPath+"/agents",
				map[string]any{"profile": fx.profileID, "config": fx.configID},
				requestOptions{
					useAuth:        true,
					idempotencyKey: keys[i],
					scope:          scopes[i],
					note:           "parallel create",
				})
			if err != nil {
				results[i].err = err
				return
			}
			if res.status != http.StatusCreated {
				results[i].err = fmt.Errorf("expected status 201, got %d\n%s", res.status, res.describe())
				return
			}
			var decoded struct {
				Agent struct {
					ID string `json:"id"`
				} `json:"agent"`
			}
			if err := json.Unmarshal(res.body, &decoded); err != nil || decoded.Agent.ID == "" {
				results[i].err = fmt.Errorf("decode launch response: %v\n%s", err, res.describe())
				return
			}
			results[i].agentID = decoded.Agent.ID
		}()
	}
	wg.Wait()

	seen := map[string]bool{}
	for i, r := range results {
		if r.err != nil {
			t.Errorf("concurrent create %d failed: %v", i, r.err)
			continue
		}
		registerAgentCleanup(t, r.agentID)
		if seen[r.agentID] {
			t.Errorf("concurrent create %d returned duplicate agent id %s", i, r.agentID)
		}
		seen[r.agentID] = true
	}
	if t.Failed() {
		return
	}

	listed := api(t, "list agents, verify all present",
		http.MethodGet, fx.projectPath+"/agents?limit=100", nil).
		requireStatus(t, http.StatusOK).json(t)
	inList := map[string]bool{}
	for _, raw := range listed["data"].([]any) {
		agent := raw.(map[string]any)
		if id, ok := agent["id"].(string); ok {
			inList[id] = true
		}
	}
	for id := range seen {
		if !inList[id] {
			t.Errorf("agent %s created concurrently is missing from the project agent list", id)
		}
	}
}
