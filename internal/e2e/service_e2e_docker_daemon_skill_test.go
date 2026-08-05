//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	harnesstools "github.com/omnara-ai/omnara/internal/harness/tools"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func TestServiceE2EDockerDaemonSkillBroadcastDeterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := newServiceE2EEnvironment(t, ctx, "docker-daemon-skill")
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		"docker-daemon-skill",
		"openai-prod",
		"service-e2e-local",
		"run_command",
	)

	nonce := strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
	canaryToken := "CANARY_" + nonce
	canaryTokenV2 := "CANARY_V2_" + nonce
	skillName := "canary-skill"
	skillArchive := buildSkillTarGz(t, skillName, canaryToken)
	skill := project.uploadProjectSkill(t, ctx, "skill-"+env.seed, skillName, skillArchive)
	if skill.Revision != 1 {
		t.Fatalf("first upload revision = %d, want 1", skill.Revision)
	}
	var currentSkill atomic.Pointer[uploadedProjectSkill]
	currentSkill.Store(&skill)

	machine := project.bootstrapDockerMachine(t, ctx, "deterministic-skill-machine")
	project.updateAgentProfileConfigWithMachineAndSkill(
		t,
		ctx,
		"skill-config-"+env.seed,
		"openai-prod",
		"service-e2e-local",
		machine.machineName,
		"/work",
		skill.ID,
		"run_command",
	)
	agentID := project.createAgent(t, ctx)

	daemon := env.startDaemonContainer(
		t,
		ctx,
		machine.daemonToken,
		machine.workdir,
	)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)

	var requestCount atomic.Int64
	verificationProcesses := make(chan string, 2)
	modelFailures := make(chan string, 1)
	failModelRequest := func(w http.ResponseWriter, status int, format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		select {
		case modelFailures <- message:
		default:
		}
		http.Error(w, message, status)
	}
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer service-e2e-test-key" {
			failModelRequest(w, http.StatusUnauthorized, "unexpected OpenAI auth header %q", auth)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			failModelRequest(w, http.StatusBadRequest, "decode OpenAI request: %v", err)
			return
		}
		if body["model"] != "service-e2e-local" {
			failModelRequest(w, http.StatusBadRequest, "unexpected model in OpenAI request: %+v", body)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch step := requestCount.Add(1); step {
		case 1:
			if !fakeModelRequestContainsTools(w, body, failModelRequest, "skill", "run_command") {
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_skill", "call_skill", "skill", map[string]any{"name": skillName})
		case 2:
			if !fakeModelToolOutputContains(w, body, failModelRequest, "call_skill", "Installed on:") {
				return
			}
			if !fakeModelToolOutputContains(w, body, failModelRequest, "call_skill", skillName) {
				return
			}
			if !fakeModelToolOutputContains(
				w,
				body,
				failModelRequest,
				"call_skill",
				harnesstools.SkillInstallPath(skill.ID, skill.RevisionID),
			) {
				return
			}
			command, err := skillRevisionCanaryCommand(skill, canaryToken)
			if err != nil {
				failModelRequest(w, http.StatusBadRequest, "resolve first skill revision: %v", err)
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_verify", "call_verify", "run_command", map[string]any{
				"command": command,
				"shell":   "sh",
				"cwd":     "/work",
				"wait_ms": 3000,
			})
		case 3:
			processID, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_verify")
			if !ok {
				return
			}
			verificationProcesses <- processID
			writeOpenAIMessage(w, failModelRequest, "resp_done", "SKILL_E2E_DONE_"+nonce)
		case 4:
			writeOpenAIFunctionCall(w, failModelRequest, "resp_skill_v2", "call_skill_v2", "skill", map[string]any{"name": skillName})
		case 5:
			if !fakeModelToolOutputContains(w, body, failModelRequest, "call_skill_v2", "Installed on:") {
				return
			}
			expectedSkill := currentSkill.Load()
			if expectedSkill == nil {
				failModelRequest(w, http.StatusInternalServerError, "second skill revision is unavailable")
				return
			}
			if !fakeModelToolOutputContains(
				w,
				body,
				failModelRequest,
				"call_skill_v2",
				harnesstools.SkillInstallPath(expectedSkill.ID, expectedSkill.RevisionID),
			) {
				return
			}
			command, err := skillRevisionCanaryCommand(*expectedSkill, canaryTokenV2)
			if err != nil {
				failModelRequest(w, http.StatusBadRequest, "resolve second skill revision: %v", err)
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_verify_v2", "call_verify_v2", "run_command", map[string]any{
				"command": command,
				"shell":   "sh",
				"cwd":     "/work",
				"wait_ms": 3000,
			})
		case 6:
			processID, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_verify_v2")
			if !ok {
				return
			}
			verificationProcesses <- processID
			writeOpenAIMessage(w, failModelRequest, "resp_done_v2", "SKILL_E2E_V2_DONE_"+nonce)
		default:
			failModelRequest(w, http.StatusTeapot, "unexpected OpenAI request %d: %s", step, mustJSONString(body))
		}
	}))
	defer openai.Close()

	project.createInput(t, ctx, agentID, "install the canary skill and verify it landed on the machine")
	project.startPermissionAutoApprover(t, ctx, agentID)
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        openai.URL,
		PublicURL:      env.containerAPIURL,
		LogLevel:       "info",
	})

	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("deterministic fake model assertion failed: %s", failure)
		default:
		}
		var count int
		err := env.db.QueryRow(ctx, `
			SELECT count(*)
			FROM agent_events event
			JOIN agents agent ON agent.id = event.agent_id
			JOIN content_blocks block
			  ON block.agent_id = event.agent_id
			 AND block.owner_model_output_id = event.model_output_id
			WHERE agent.project_id = $1
			  AND event.agent_id = $2
			  AND event.event_kind = 'model_output'
			  AND block.block_kind = 'text'
			  AND block.text_content LIKE '%' || $3 || '%'
		`, projectUUID, agentUUID, "SKILL_E2E_DONE_"+nonce).Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		if count == 1 {
			return true, ""
		}
		var wakeups, locks, inputs int
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).Scan(&wakeups)
		_ = env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).Scan(&locks)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).Scan(&inputs)
		var toolStates string
		_ = env.db.QueryRow(ctx, `
SELECT coalesce(string_agg(call.name || ':' || call.state, ',' ORDER BY call.created_at), '')
FROM tool_call_read_projection call
WHERE call.project_id = $1 AND call.agent_id = $2
`, projectUUID, agentUUID).Scan(&toolStates)
		return false, fmt.Sprintf(
			"assistant token missing requests=%d wakeups=%d locks=%d inputs=%d tools=%s api=%s worker=%s daemon=%s",
			requestCount.Load(), wakeups, locks, inputs, toolStates,
			api.logExcerpt(), worker.logExcerpt(), daemon.logExcerpt(),
		)
	})

	assertSkillVerificationProcessSucceeded(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		nextSkillVerificationProcess(t, ctx, verificationProcesses),
	)
	if got := requestCount.Load(); got != 3 {
		t.Fatalf("deterministic OpenAI server saw %d first-turn requests, want 3", got)
	}
	select {
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	default:
	}

	// Upload a second revision under the same skill name and drive another
	// skill invocation: the daemon must replace the installed content with
	// the new revision without any agent-config change.
	skillArchiveV2 := buildSkillTarGz(t, skillName, canaryTokenV2)
	skillV2 := project.uploadProjectSkill(t, ctx, "skill-v2-"+env.seed, skillName, skillArchiveV2)
	if skillV2.ID != skill.ID {
		t.Fatalf("second upload created a new skill id %s, want %s", skillV2.ID, skill.ID)
	}
	if skillV2.Revision != 2 {
		t.Fatalf("second upload revision = %d, want 2", skillV2.Revision)
	}
	currentSkill.Store(&skillV2)

	project.createInput(t, ctx, agentID, "the canary skill was updated; reinstall it and verify the new content")

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("deterministic fake model assertion failed: %s", failure)
		default:
		}
		var count int
		err := env.db.QueryRow(ctx, `
			SELECT count(*)
			FROM agent_events event
			JOIN agents agent ON agent.id = event.agent_id
			JOIN content_blocks block
			  ON block.agent_id = event.agent_id
			 AND block.owner_model_output_id = event.model_output_id
			WHERE agent.project_id = $1
			  AND event.agent_id = $2
			  AND event.event_kind = 'model_output'
			  AND block.block_kind = 'text'
			  AND block.text_content LIKE '%' || $3 || '%'
		`, projectUUID, agentUUID, "SKILL_E2E_V2_DONE_"+nonce).Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		if count == 1 {
			return true, ""
		}
		return false, fmt.Sprintf(
			"v2 assistant token missing requests=%d api=%s worker=%s daemon=%s",
			requestCount.Load(), api.logExcerpt(), worker.logExcerpt(), daemon.logExcerpt(),
		)
	})

	assertSkillVerificationProcessSucceeded(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		nextSkillVerificationProcess(t, ctx, verificationProcesses),
	)
	if got := requestCount.Load(); got != 6 {
		t.Fatalf("deterministic OpenAI server saw %d total requests, want 6", got)
	}
	select {
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	default:
	}
}

func nextSkillVerificationProcess(
	t *testing.T,
	ctx context.Context,
	processes <-chan string,
) string {
	t.Helper()
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case processID := <-processes:
		return processID
	}
	return ""
}

func assertSkillVerificationProcessSucceeded(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, processID string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	processUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProcess, processID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var state executionstore.ProcessState
		var exitCode int
		if err := env.db.QueryRow(ctx, `
SELECT state, coalesce(exit_code, -1)
FROM processes
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, projectUUID, agentUUID, processUUID).Scan(&state, &exitCode); err != nil {
			return false, err.Error()
		}
		switch state {
		case executionstore.ProcessStateExited:
			if exitCode != 0 {
				t.Fatalf("skill verification process exited with code %d", exitCode)
			}
			return true, ""
		case executionstore.ProcessStateFailed,
			executionstore.ProcessStateKilled,
			executionstore.ProcessStateUnknown:
			t.Fatalf("skill verification process ended in state %q with code %d", state, exitCode)
		}
		return false, fmt.Sprintf("skill verification process state=%q", state)
	})
}

func skillRevisionCanaryCommand(
	skill uploadedProjectSkill,
	wantCanary string,
) (string, error) {
	installPath := harnesstools.SkillInstallPath(skill.ID, skill.RevisionID)
	relativePath, found := strings.CutPrefix(installPath, "$OMNARA_HOME")
	if !found {
		return "", fmt.Errorf("skill install path %q is not rooted at $OMNARA_HOME", installPath)
	}
	return `test "$(cat "$OMNARA_HOME"` + relativePath + `/CANARY.txt)" = ` + quoteShellWord(wantCanary), nil
}

func quoteShellWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
