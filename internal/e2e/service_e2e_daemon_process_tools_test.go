//go:build integration && servicee2e

package e2e

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var processToolNames = []string{
	"run_command",
	"write_process",
	"read_process",
	"stop_process",
	"list_processes",
}

var processToolsRequiringApproval = map[string]struct{}{
	"run_command":   {},
	"write_process": {},
	"stop_process":  {},
}

func TestServiceE2EDockerDaemonProcessToolsDeterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := newServiceE2EEnvironment(t, ctx, "docker-daemon-process-tools")
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		"docker-daemon-process-tools",
		"openai-prod",
		"service-e2e-local",
		processToolNames...)
	nonce := "DOCKER_DAEMON_PROCESS_TOOLS_" + strings.ToUpper(strings.ReplaceAll(env.seed, "-", "_"))
	literalEnv := "literal_" + nonce
	secretEnv := "secret_" + nonce
	secret := env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+project.orgID+"/secrets",
		map[string]any{
			"owner": map[string]any{
				"kind":       "project",
				"project_id": project.projectID,
			},
			"name": "daemon-process-environment",
			"material": map[string]string{
				"kind":  "generic",
				"value": secretEnv,
			},
		},
		"",
		project.adminToken,
		http.StatusCreated,
	)
	secretID := secret["id"].(string)
	quickCommandOutput := strings.Join([]string{nonce, literalEnv, secretEnv}, "|")
	missingCwd := "/work/missing-" + nonce
	machine := project.bootstrapDockerMachine(t, ctx, "deterministic-byo-machine")
	project.updateAgentProfileConfigWithMachine(
		t,
		ctx,
		"docker-daemon-process-tools-machine",
		"openai-prod",
		"service-e2e-local",
		machine.machineName,
		"/work",
		[]string{
			"    env_overlay:",
			"      SERVICE_E2E_LITERAL: " + literalEnv,
			"    secret_env_overlay:",
			"      SERVICE_E2E_SECRET: " + secretID,
		},
		processToolsRequiringApproval,
		processToolNames...)
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
	var mu sync.Mutex
	var processID string
	var processCursor int64
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
		step := requestCount.Add(1)
		t.Logf("deterministic daemon fake model request step=%d", step)
		switch step {
		case 1:
			if !fakeModelRequestContainsTools(w, body, failModelRequest, processToolNames...) {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_invalid_cwd",
				"call_invalid_cwd",
				"run_command",
				map[string]any{
					"command": "echo should-not-run",
					"cwd":     missingCwd,
					"wait_ms": 1000,
				},
			)
		case 2:
			if !fakeModelProcessToolResult(
				w,
				body,
				failModelRequest,
				"call_invalid_cwd",
				"failed",
			) || !fakeModelToolOutputContains(
				w,
				body,
				failModelRequest,
				"call_invalid_cwd",
				missingCwd,
			) {
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_run_command", "call_run_command", "run_command", map[string]any{
				"command": fmt.Sprintf("printf '%%s|%%s|%%s\\n' '%s' \"$SERVICE_E2E_LITERAL\" \"$SERVICE_E2E_SECRET\" > quick-command.txt", nonce),
				"wait_ms": 1000,
			})
		case 3:
			if _, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_run_command"); !ok {
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_start", "call_start", "run_command", map[string]any{
				"command": "while IFS= read -r line; do case \"$line\" in CREATE*) printf created > stdin-file.txt; printf C;; DELETE*) rm -f stdin-file.txt; printf D;; esac; done",
				"shell":   "sh",
				"cwd":     "/work",
				"wait_ms": 1000,
			})
		case 4:
			id, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_start")
			if !ok {
				return
			}
			mu.Lock()
			processID = id
			mu.Unlock()
			if !fakeModelWaitForProcessGrantedToDaemon(ctx, w, env, failModelRequest, project.projectID, agentID, id) {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_write_create",
				"call_write_create",
				"write_process",
				map[string]any{"process_id": id, "data": "CREATE\n"},
			)
		case 5:
			if !fakeModelProcessActionAccepted(w, body, failModelRequest, "call_write_create") {
				return
			}
			id := currentProcessID(&mu, &processID)
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_read_create",
				"call_read_create",
				"read_process",
				map[string]any{"process_id": id, "cursor": 0, "max_bytes": 4096, "wait_ms": 1000},
			)
		case 6:
			result, ok := fakeModelProcessResult(w, body, failModelRequest, "call_read_create")
			if !ok || result.Output != "C" || result.NextCursor == nil {
				if ok {
					failModelRequest(w, http.StatusBadRequest, "create read result is incomplete: %+v", result)
				}
				return
			}
			mu.Lock()
			processCursor = *result.NextCursor
			id := processID
			mu.Unlock()
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_write_delete",
				"call_write_delete",
				"write_process",
				map[string]any{"process_id": id, "data": "DELETE\n"},
			)
		case 7:
			if !fakeModelProcessActionAccepted(w, body, failModelRequest, "call_write_delete") {
				return
			}
			mu.Lock()
			id := processID
			cursor := processCursor
			mu.Unlock()
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_read_delete",
				"call_read_delete",
				"read_process",
				map[string]any{"process_id": id, "cursor": cursor, "max_bytes": 4096, "wait_ms": 1000},
			)
		case 8:
			result, ok := fakeModelProcessResult(w, body, failModelRequest, "call_read_delete")
			if !ok || result.Output != "D" || result.NextCursor == nil {
				if ok {
					failModelRequest(w, http.StatusBadRequest, "delete read result is incomplete: %+v", result)
				}
				return
			}
			mu.Lock()
			processCursor = *result.NextCursor
			id := processID
			cursor := processCursor
			mu.Unlock()
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_wait_running",
				"call_wait_running",
				"read_process",
				map[string]any{
					"process_id": id,
					"cursor":     cursor,
					"max_bytes":  4096,
					"wait_ms":    100,
				},
			)
		case 9:
			result, ok := fakeModelProcessResult(w, body, failModelRequest, "call_wait_running")
			if !ok || result.State != "running" || result.NextCursor == nil {
				if ok {
					failModelRequest(w, http.StatusBadRequest, "running read result is incomplete: %+v", result)
				}
				return
			}
			mu.Lock()
			processCursor = *result.NextCursor
			id := processID
			mu.Unlock()
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_close_stdin",
				"call_close_stdin",
				"write_process",
				map[string]any{"process_id": id, "close_stdin": true},
			)
		case 10:
			if !fakeModelProcessActionAccepted(w, body, failModelRequest, "call_close_stdin") {
				return
			}
			mu.Lock()
			id := processID
			cursor := processCursor
			mu.Unlock()
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_read_terminal",
				"call_read_terminal",
				"read_process",
				map[string]any{
					"process_id": id,
					"cursor":     cursor,
					"max_bytes":  4096,
					"wait_ms":    1000,
				},
			)
		case 11:
			if !fakeModelProcessToolResult(w, body, failModelRequest, "call_read_terminal", "exited") {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_start_interrupt",
				"call_start_interrupt",
				"run_command",
				map[string]any{
					"command": "trap 'printf interrupted > /work/interrupted.txt; exit 130' INT; printf R; sleep 20; printf M",
					"shell":   "sh",
					"cwd":     "/work",
					"wait_ms": 1000,
				},
			)
		case 12:
			id, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_start_interrupt")
			if !ok {
				return
			}
			if !fakeModelWaitForProcessGrantedToDaemon(ctx, w, env, failModelRequest, project.projectID, agentID, id) {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_read_ready",
				"call_read_ready",
				"read_process",
				map[string]any{"process_id": id, "cursor": 0, "max_bytes": 4096, "wait_ms": 1000},
			)
		case 13:
			if !fakeModelToolOutputContains(w, body, failModelRequest, "call_read_ready", "R") {
				return
			}
			id, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_start_interrupt")
			if !ok {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_interrupt",
				"call_interrupt",
				"stop_process",
				map[string]any{"process_id": id, "mode": "interrupt"},
			)
		case 14:
			if !fakeModelProcessActionAccepted(w, body, failModelRequest, "call_interrupt") {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_start_terminate",
				"call_start_terminate",
				"run_command",
				map[string]any{
					"command": "echo TERMINATE_READY; while :; do sleep 1; done",
					"shell":   "sh",
					"cwd":     "/work",
					"wait_ms": 1000,
				},
			)
		case 15:
			id, ok := fakeModelToolProcessID(w, body, failModelRequest, "call_start_terminate")
			if !ok {
				return
			}
			if !fakeModelWaitForProcessGrantedToDaemon(ctx, w, env, failModelRequest, project.projectID, agentID, id) {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_terminate",
				"call_terminate",
				"stop_process",
				map[string]any{"process_id": id, "mode": "terminate"},
			)
		case 16:
			if !fakeModelProcessActionAccepted(w, body, failModelRequest, "call_terminate") {
				return
			}
			writeOpenAIFunctionCall(w, failModelRequest, "resp_list", "call_list", "list_processes", map[string]any{})
		case 17:
			if !fakeModelToolOutputContains(w, body, failModelRequest, "call_list", "processes") {
				return
			}
			writeOpenAIMessage(w, failModelRequest, "resp_done", "DAEMON_E2E_DONE_"+nonce)
		default:
			failModelRequest(w, http.StatusTeapot, "unexpected extra OpenAI request: %s", mustJSONString(body))
		}
	}))
	defer openai.Close()

	project.createInput(t, ctx, agentID, "exercise every daemon-backed process tool")
	project.startPermissionAutoApprover(t, ctx, agentID)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf("deterministic fake model assertion failed: %s", failure)
		default:
		}
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content LIKE '%' || $3 || '%'`, projectUUID, agentUUID, "DAEMON_E2E_DONE_"+nonce).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		if count == 1 {
			return true, ""
		}
		var wakeups, locks, inputs int
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
			Scan(&wakeups)
		_ = env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&inputs)
		var toolStates, interactionStates, processStates string
		_ = env.db.QueryRow(ctx, `
SELECT coalesce(string_agg(call.name || ':' || call.state, ',' ORDER BY call.created_at), '')
FROM tool_call_read_projection call
WHERE call.project_id = $1 AND call.agent_id = $2
`, projectUUID, agentUUID).
			Scan(&toolStates)
		_ = env.db.QueryRow(ctx, `SELECT coalesce(string_agg(interaction_kind || ':' || state, ',' ORDER BY created_at), '') FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&interactionStates)
		_ = env.db.QueryRow(ctx, `SELECT coalesce(string_agg(state || ':granted=' || (execution_granted_at IS NOT NULL)::text, ',' ORDER BY created_at), '') FROM processes WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&processStates)
		var processDetails string
		_ = env.db.QueryRow(ctx, `SELECT coalesce(string_agg(command || ':timeout=' || timeout_seconds::text || ':shell=' || shell_selector, ',' ORDER BY created_at), '') FROM processes WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&processDetails)
		return false, fmt.Sprintf(
			"assistant token missing requests=%d wakeups=%d locks=%d inputs=%d tools=%s interactions=%s processes=%s processDetails=%s api=%s worker=%s daemon=%s",
			requestCount.Load(),
			wakeups,
			locks,
			inputs,
			toolStates,
			interactionStates,
			processStates,
			processDetails,
			api.logExcerpt(),
			worker.logExcerpt(),
			daemon.logExcerpt(),
		)
	})
	assertDockerDaemonProcessEvidence(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		machine.machineID,
		machine.workdir,
		quickCommandOutput,
		missingCwd,
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var locks, wakeups int
		if err := env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
			Scan(&wakeups); err != nil {
			return false, err.Error()
		}
		return locks == 0 && wakeups == 0, "runtime lock or wakeup still present; daemon logs=" + daemon.logExcerpt()
	})
	if got := requestCount.Load(); got != 17 {
		t.Fatalf("deterministic OpenAI server saw %d requests, want 17", got)
	}
	select {
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	default:
	}
}

func TestServiceE2EDaemonContainerCrashDoesNotRepeatAcceptedProcess(
	t *testing.T,
) {
	runServiceE2ERecoveryFaultJourney(t, serviceE2ERecoveryFaultScenario{
		seed:             "daemon-container-crash-recovery",
		command:          "printf x >> /work/recovery-effect.txt; sleep 30",
		finalText:        "DAEMON_CONTAINER_RECOVERY_DONE",
		wantProcessState: "unknown",
		wantReasonCode:   "local_process_missing_after_daemon_reconnect",
		fault:            serviceE2ECrashDaemonContainer,
	})
}

func TestServiceE2ECompleteDaemonStateLossReconcilesAndAcceptsNewWork(
	t *testing.T,
) {
	runServiceE2ERecoveryFaultJourney(t, serviceE2ERecoveryFaultScenario{
		seed:             "complete-daemon-state-loss",
		command:          "printf x >> /work/recovery-effect.txt; sleep 30",
		finalText:        "DAEMON_STATE_LOSS_DONE",
		wantProcessState: "unknown",
		wantReasonCode:   "local_process_missing_after_daemon_reconnect",
		fault:            serviceE2ELoseDaemonState,
		freshWork: &serviceE2EFreshWork{
			command:    "printf y >> /work/fresh-effect.txt",
			effectFile: "fresh-effect.txt",
			effect:     "y",
		},
	})
}

func TestServiceE2ECancelAcceptedRunCommandStopsProcessWithoutReoffer(
	t *testing.T,
) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	const (
		seed           = "cancel-accepted-run-command"
		providerCallID = "call_cancel_accepted_process"
	)
	env := newServiceE2EEnvironment(t, ctx, seed)
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		seed,
		"openai-prod",
		"service-e2e-local",
		"run_command",
	)
	machine := project.bootstrapDockerMachine(t, ctx, seed+"-machine")
	project.updateAgentProfileConfigWithMachine(
		t,
		ctx,
		seed+"-config",
		"openai-prod",
		"service-e2e-local",
		machine.machineName,
		"/work",
		nil,
		processToolsRequiringApproval,
		"run_command",
	)
	agentID := project.createAgent(t, ctx)
	stateVolume := env.createDockerVolume(t, "daemon-state")
	daemon := env.startDaemonContainerWithStateVolume(
		t,
		ctx,
		machine.daemonToken,
		machine.workdir,
		stateVolume,
	)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)

	var requestCount atomic.Int64
	modelFailures := make(chan string, 1)
	failModelRequest := func(
		w http.ResponseWriter,
		status int,
		format string,
		args ...any,
	) {
		message := fmt.Sprintf(format, args...)
		select {
		case modelFailures <- message:
		default:
		}
		http.Error(w, message, status)
	}
	openai := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			failModelRequest(
				w,
				http.StatusBadRequest,
				"decode OpenAI request: %v",
				err,
			)
			return
		}
		if step := requestCount.Add(1); step != 1 {
			failModelRequest(
				w,
				http.StatusTeapot,
				"unexpected OpenAI request %d: %s",
				step,
				mustJSONString(body),
			)
			return
		}
		if !fakeModelRequestContainsTools(
			w,
			body,
			failModelRequest,
			"run_command",
		) {
			return
		}
		writeOpenAIFunctionCall(
			w,
			failModelRequest,
			"resp_cancel_accepted_process",
			providerCallID,
			"run_command",
			map[string]any{
				"command": "printf '%s\\n' \"$$\" > /work/cancel-parent.pid; " +
					"sleep 60 & child=$!; " +
					"printf '%s\\n' \"$child\" > /work/cancel-child.pid; " +
					"printf x >> /work/cancel-effect.txt; " +
					"wait \"$child\"; printf y >> /work/cancel-effect.txt",
				"shell":   "sh",
				"cwd":     "/work",
				"wait_ms": 10000,
			},
		)
	}))
	defer openai.Close()

	project.createInput(
		t,
		ctx,
		agentID,
		"start the command and wait for its result",
	)
	project.startPermissionAutoApprover(t, ctx, agentID)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{
			ProviderConfig: "openai-prod",
			BaseURL:        openai.URL,
		},
	)
	markerPath := filepath.Join(machine.workdir, "cancel-effect.txt")
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		body, err := os.ReadFile(markerPath)
		if err == nil && string(body) == "x" {
			return true, ""
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err.Error()
		}
		return false, fmt.Sprintf(
			"accepted command has not started; api=%s worker=%s daemon=%s",
			api.logExcerpt(),
			worker.logExcerpt(),
			daemon.logExcerpt(),
		)
	})
	processID, priorRuntimeID := waitForServiceE2EGrantedProcess(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
	)

	cancelResponse := env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		project.projectPath+"/agents/"+agentID+"/cancel",
		map[string]any{},
		"",
		project.adminToken,
		http.StatusOK,
	)
	if cancelResponse["affected"] != true {
		t.Fatalf(
			"cancel response = %+v, want affected cancellation",
			cancelResponse,
		)
	}
	waitForServiceE2EProcessState(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		processID,
		"unknown",
		"agent_canceled_after_grant",
	)
	waitForServiceE2ECanceledToolCall(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		providerCallID,
	)

	parentPID := readServiceE2EPID(
		t,
		filepath.Join(machine.workdir, "cancel-parent.pid"),
	)
	childPID := readServiceE2EPID(
		t,
		filepath.Join(machine.workdir, "cancel-child.pid"),
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		script := `
not_running() {
	[ ! -r "/proc/$1/status" ] && return 0
	state=$(awk '$1 == "State:" { print $2 }' "/proc/$1/status")
	[ "$state" = "Z" ]
}
not_running "$1" && not_running "$2"
`
		output, err := exec.CommandContext(
			ctx,
			"docker",
			"exec",
			daemon.containerName,
			"sh",
			"-ceu",
			script,
			"cancel-accepted-run-command",
			parentPID,
			childPID,
		).CombinedOutput()
		if err == nil {
			return true, ""
		}
		return false, fmt.Sprintf(
			"canceled command tree still exists parent=%s child=%s output=%s",
			parentPID,
			childPID,
			output,
		)
	})
	assertServiceE2EEffectOnce(t, markerPath)

	daemon.crashContainer(t, ctx)
	daemon = env.startDaemonContainerWithStateVolume(
		t,
		ctx,
		machine.daemonToken,
		machine.workdir,
		stateVolume,
	)
	waitForReplacementServiceE2EDaemonRuntime(
		t,
		ctx,
		env,
		project.orgID,
		machine.machineID,
		priorRuntimeID,
	)
	waitForServiceE2EProcessState(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		processID,
		"unknown",
		"agent_canceled_after_grant",
	)
	assertServiceE2ESingleProcess(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		"unknown",
	)
	assertServiceE2EEffectOnce(t, markerPath)
	if got := requestCount.Load(); got != 1 {
		t.Fatalf("deterministic OpenAI server saw %d requests, want 1", got)
	}
	select {
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	default:
	}
}

func TestServiceE2EManagedDaemonCrashRecoversAcceptedProcess(t *testing.T) {
	runServiceE2ERecoveryFaultJourney(t, serviceE2ERecoveryFaultScenario{
		seed: "managed-daemon-crash-recovery",
		command: "printf x >> /work/recovery-effect.txt; " +
			"while [ ! -f /work/release-process ]; do sleep 1; done",
		finalText:        "MANAGED_DAEMON_RECOVERY_DONE",
		wantProcessState: "exited",
		fault:            serviceE2ECrashManagedDaemon,
	})
}

func TestServiceE2EProcessSupervisorCrashDoesNotRepeatAcceptedProcess(
	t *testing.T,
) {
	runServiceE2ERecoveryFaultJourney(t, serviceE2ERecoveryFaultScenario{
		seed:             "process-supervisor-crash-recovery",
		command:          "printf x >> /work/recovery-effect.txt; sleep 30",
		finalText:        "PROCESS_SUPERVISOR_RECOVERY_DONE",
		wantProcessState: "unknown",
		wantReasonCode:   "local_process_missing_after_daemon_reconnect",
		fault:            serviceE2ECrashProcessSupervisor,
	})
}

type serviceE2ERecoveryFault uint8

const (
	serviceE2ECrashDaemonContainer serviceE2ERecoveryFault = iota + 1
	serviceE2ECrashManagedDaemon
	serviceE2ECrashProcessSupervisor
	serviceE2ELoseDaemonState
)

type serviceE2ERecoveryFaultScenario struct {
	seed             string
	command          string
	finalText        string
	wantProcessState string
	wantReasonCode   string
	fault            serviceE2ERecoveryFault
	freshWork        *serviceE2EFreshWork
}

type serviceE2EFreshWork struct {
	command    string
	effectFile string
	effect     string
}

func runServiceE2ERecoveryFaultJourney(
	t *testing.T,
	scenario serviceE2ERecoveryFaultScenario,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	env := newServiceE2EEnvironment(t, ctx, scenario.seed)
	api := env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		scenario.seed,
		"openai-prod",
		"service-e2e-local",
		"run_command",
	)
	machine := project.bootstrapDockerMachine(
		t,
		ctx,
		scenario.seed+"-machine",
	)
	project.updateAgentProfileConfigWithMachine(
		t,
		ctx,
		scenario.seed+"-config",
		"openai-prod",
		"service-e2e-local",
		machine.machineName,
		"/work",
		nil,
		processToolsRequiringApproval,
		"run_command",
	)
	agentID := project.createAgent(t, ctx)
	stateVolume := env.createDockerVolume(t, "daemon-state")
	daemon := env.startDaemonContainerWithStateVolume(
		t,
		ctx,
		machine.daemonToken,
		machine.workdir,
		stateVolume,
	)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)

	var requestCount atomic.Int64
	modelFailures := make(chan string, 1)
	failModelRequest := func(
		w http.ResponseWriter,
		status int,
		format string,
		args ...any,
	) {
		message := fmt.Sprintf(format, args...)
		select {
		case modelFailures <- message:
		default:
		}
		http.Error(w, message, status)
	}
	openai := httptest.NewServer(http.HandlerFunc(func(
		w http.ResponseWriter,
		r *http.Request,
	) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			failModelRequest(
				w,
				http.StatusBadRequest,
				"decode OpenAI request: %v",
				err,
			)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch step := requestCount.Add(1); step {
		case 1:
			if !fakeModelRequestContainsTools(
				w,
				body,
				failModelRequest,
				"run_command",
			) {
				return
			}
			writeOpenAIFunctionCall(
				w,
				failModelRequest,
				"resp_recovery_process",
				"call_recovery_process",
				"run_command",
				map[string]any{
					"command": scenario.command,
					"shell":   "sh",
					"cwd":     "/work",
					"wait_ms": 10000,
				},
			)
		case 2:
			if !fakeModelProcessToolResult(
				w,
				body,
				failModelRequest,
				"call_recovery_process",
				scenario.wantProcessState,
			) {
				return
			}
			if scenario.freshWork != nil {
				writeOpenAIFunctionCall(
					w,
					failModelRequest,
					"resp_recovery_fresh_work",
					"call_recovery_fresh_work",
					"run_command",
					map[string]any{
						"command": scenario.freshWork.command,
						"shell":   "sh",
						"cwd":     "/work",
						"wait_ms": 1000,
					},
				)
				return
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_recovery_done",
				scenario.finalText,
			)
		case 3:
			if scenario.freshWork == nil {
				failModelRequest(
					w,
					http.StatusTeapot,
					"unexpected extra OpenAI request: %s",
					mustJSONString(body),
				)
				return
			}
			if !fakeModelProcessToolResult(
				w,
				body,
				failModelRequest,
				"call_recovery_fresh_work",
				"exited",
			) {
				return
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_recovery_done",
				scenario.finalText,
			)
		default:
			failModelRequest(
				w,
				http.StatusTeapot,
				"unexpected extra OpenAI request: %s",
				mustJSONString(body),
			)
		}
	}))
	defer openai.Close()

	project.createInput(
		t,
		ctx,
		agentID,
		"run the command exactly once across the injected daemon fault",
	)
	project.startPermissionAutoApprover(t, ctx, agentID)
	worker := env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{
			ProviderConfig: "openai-prod",
			BaseURL:        openai.URL,
		},
	)
	markerPath := filepath.Join(machine.workdir, "recovery-effect.txt")
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		body, err := os.ReadFile(markerPath)
		if err == nil && string(body) == "x" {
			return true, ""
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err.Error()
		}
		return false, fmt.Sprintf(
			"accepted process has not crossed its external effect; api=%s worker=%s daemon=%s",
			api.logExcerpt(),
			worker.logExcerpt(),
			daemon.logExcerpt(),
		)
	})
	processID, priorRuntimeID := waitForServiceE2EGrantedProcess(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
	)

	switch scenario.fault {
	case serviceE2ECrashDaemonContainer:
		daemon.crashContainer(t, ctx)
		daemon = env.startDaemonContainerWithStateVolume(
			t,
			ctx,
			machine.daemonToken,
			machine.workdir,
			stateVolume,
		)
		waitForReplacementServiceE2EDaemonRuntime(
			t,
			ctx,
			env,
			project.orgID,
			machine.machineID,
			priorRuntimeID,
		)
	case serviceE2ECrashManagedDaemon:
		daemon.crashManagedDaemon(t, ctx)
		if err := os.WriteFile(
			filepath.Join(machine.workdir, "release-process"),
			[]byte("release"),
			0o600,
		); err != nil {
			t.Fatalf("release recovered process: %v", err)
		}
		waitForReplacementServiceE2EDaemonRuntime(
			t,
			ctx,
			env,
			project.orgID,
			machine.machineID,
			priorRuntimeID,
		)
	case serviceE2ECrashProcessSupervisor:
		daemon.crashProcessSupervisor(t, ctx, processID)
	case serviceE2ELoseDaemonState:
		daemon.crashContainer(t, ctx)
		env.recreateDockerVolume(t, stateVolume)
		daemon = env.startDaemonContainerWithStateVolume(
			t,
			ctx,
			machine.daemonToken,
			machine.workdir,
			stateVolume,
		)
		waitForReplacementServiceE2EDaemonRuntime(
			t,
			ctx,
			env,
			project.orgID,
			machine.machineID,
			priorRuntimeID,
		)
	default:
		t.Fatalf("unsupported recovery fault %d", scenario.fault)
	}
	waitForServiceE2ERecoveryAssistantText(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		scenario.finalText,
		modelFailures,
		requestCount.Load,
		api,
		worker,
		daemon,
	)
	waitForServiceE2EProcessState(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		processID,
		scenario.wantProcessState,
		scenario.wantReasonCode,
	)
	if scenario.fault == serviceE2ECrashProcessSupervisor {
		waitForServiceE2EActiveDaemonRuntime(
			t,
			ctx,
			env,
			project.orgID,
			machine.machineID,
			priorRuntimeID,
		)
	}
	assertServiceE2EEffectOnce(t, markerPath)
	assertServiceE2EProcessToolResult(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		"call_recovery_process",
		scenario.wantProcessState,
	)
	wantRequests := int64(2)
	if scenario.freshWork == nil {
		assertServiceE2ESingleProcess(
			t,
			ctx,
			env,
			project.projectID,
			agentID,
			scenario.wantProcessState,
		)
	} else {
		wantRequests = 3
		assertServiceE2EFreshWork(
			t,
			ctx,
			env,
			project.projectID,
			agentID,
			machine.workdir,
			*scenario.freshWork,
		)
	}
	if got := requestCount.Load(); got != wantRequests {
		t.Fatalf(
			"deterministic OpenAI server saw %d requests, want %d",
			got,
			wantRequests,
		)
	}
	select {
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	default:
	}
}

func waitForServiceE2EGrantedProcess(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID string,
) (string, string) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProject,
		projectID,
	)
	agentUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindAgent,
		agentID,
	)
	var processUUID uuid.UUID
	var currentRuntimeID string
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var state string
		err := env.db.QueryRow(ctx, `
SELECT process.id, process.state, coalesce(runtime.id::text, '')
FROM processes process
LEFT JOIN registered_daemon_runtimes runtime
  ON runtime.org_id = process.org_id
 AND runtime.machine_id = process.machine_id
WHERE process.project_id = $1 AND process.agent_id = $2
ORDER BY process.created_at, process.id
LIMIT 1
`, projectUUID, agentUUID).Scan(&processUUID, &state, &currentRuntimeID)
		if err != nil {
			return false, err.Error()
		}
		if currentRuntimeID != "" &&
			(state == "starting" || state == "running") {
			return true, ""
		}
		return false, fmt.Sprintf(
			"process state=%q current_runtime=%q, want granted process on a registered machine runtime",
			state,
			currentRuntimeID,
		)
	})
	processID, err := publicid.Encode(publicid.KindProcess, processUUID)
	if err != nil {
		t.Fatalf("encode process public ID: %v", err)
	}
	return processID, currentRuntimeID
}

func waitForServiceE2EActiveDaemonRuntime(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	orgID, machineID, wantRuntimeID string,
) {
	t.Helper()
	waitForServiceE2EDaemonRuntime(
		t,
		ctx,
		env,
		orgID,
		machineID,
		wantRuntimeID,
		false,
	)
}

func waitForReplacementServiceE2EDaemonRuntime(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	orgID, machineID, previousRuntimeID string,
) {
	t.Helper()
	waitForServiceE2EDaemonRuntime(
		t,
		ctx,
		env,
		orgID,
		machineID,
		previousRuntimeID,
		true,
	)
}

func waitForServiceE2EDaemonRuntime(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	orgID, machineID, comparedRuntimeID string,
	wantReplacement bool,
) {
	t.Helper()
	orgUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindOrganization,
		orgID,
	)
	machineUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindMachine,
		machineID,
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		var runtimeID string
		err := env.db.QueryRow(ctx, `
SELECT count(*), coalesce(min(id::text), '')
FROM online_daemon_runtimes
WHERE org_id = $1 AND machine_id = $2
`, orgUUID, machineUUID).Scan(&count, &runtimeID)
		if err != nil {
			return false, err.Error()
		}
		if count == 1 &&
			(runtimeID != comparedRuntimeID) == wantReplacement {
			return true, ""
		}
		expectation := "retained"
		if wantReplacement {
			expectation = "replacement"
		}
		return false, fmt.Sprintf(
			"online daemon runtimes=%d active=%q, want %s of %q",
			count,
			runtimeID,
			expectation,
			comparedRuntimeID,
		)
	})
}

func waitForServiceE2ERecoveryAssistantText(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, contains string,
	modelFailures <-chan string,
	requestCount func() int64,
	api, worker, daemon serviceProcess,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProject,
		projectID,
	)
	agentUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindAgent,
		agentID,
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		select {
		case failure := <-modelFailures:
			t.Fatalf(
				"deterministic fake model assertion failed: %s",
				failure,
			)
		default:
		}
		var completed int
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
`, projectUUID, agentUUID, contains).Scan(&completed)
		if err != nil {
			return false, err.Error()
		}
		if completed == 1 {
			return true, ""
		}
		return false, fmt.Sprintf(
			"recovery result unresolved requests=%d api=%s worker=%s daemon=%s",
			requestCount(),
			api.logExcerpt(),
			worker.logExcerpt(),
			daemon.logExcerpt(),
		)
	})
}

func waitForServiceE2EProcessState(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, processID, wantState, wantReasonCode string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProject,
		projectID,
	)
	agentUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindAgent,
		agentID,
	)
	processUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProcess,
		processID,
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var state, reasonCode string
		err := env.db.QueryRow(ctx, `
SELECT state, coalesce(state_reason_code, '')
FROM processes
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, projectUUID, agentUUID, processUUID).Scan(&state, &reasonCode)
		if err != nil {
			return false, err.Error()
		}
		if state == wantState && reasonCode == wantReasonCode {
			return true, ""
		}
		return false, fmt.Sprintf(
			"process state=%q reason=%q, want %q/%q",
			state,
			reasonCode,
			wantState,
			wantReasonCode,
		)
	})
}

func assertServiceE2EEffectOnce(
	t *testing.T,
	markerPath string,
) {
	t.Helper()
	body, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "x" {
		t.Fatalf(
			"accepted process external effect = %q, want exactly one x",
			body,
		)
	}
}

func assertServiceE2EFreshWork(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, workdir string,
	fresh serviceE2EFreshWork,
) {
	t.Helper()
	effectPath := filepath.Join(workdir, fresh.effectFile)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		body, err := os.ReadFile(effectPath)
		if err == nil && string(body) == fresh.effect {
			return true, ""
		}
		return false, fmt.Sprintf(
			"fresh-work effect = %q err=%v, want %q",
			body,
			err,
			fresh.effect,
		)
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	var total, exited, unknown, granted int
	if err := env.db.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE state = 'exited'),
       count(*) FILTER (WHERE state = 'unknown'),
       count(*) FILTER (WHERE execution_granted_at IS NOT NULL)
FROM processes
WHERE project_id = $1 AND agent_id = $2
`, projectUUID, agentUUID).Scan(&total, &exited, &unknown, &granted); err != nil {
		t.Fatal(err)
	}
	if total != 2 || exited != 1 || unknown != 1 || granted != 2 {
		t.Fatalf(
			"processes total=%d exited=%d unknown=%d granted=%d, want 2/1/1/2",
			total,
			exited,
			unknown,
			granted,
		)
	}
	assertServiceE2EProcessToolResult(
		t,
		ctx,
		env,
		projectID,
		agentID,
		"call_recovery_fresh_work",
		"exited",
	)
}

func assertServiceE2ESingleProcess(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, wantState string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProject,
		projectID,
	)
	agentUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindAgent,
		agentID,
	)
	var processCount int
	var processState string
	if err := env.db.QueryRow(ctx, `
SELECT count(*), coalesce(min(state), '')
FROM processes
WHERE project_id = $1 AND agent_id = $2
`, projectUUID, agentUUID).Scan(&processCount, &processState); err != nil {
		t.Fatal(err)
	}
	if processCount != 1 || processState != wantState {
		t.Fatalf(
			"recovery process rows = %d state=%q, want one %s",
			processCount,
			processState,
			wantState,
		)
	}
}

func assertServiceE2EProcessToolResult(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, providerCallID, wantState string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProject,
		projectID,
	)
	agentUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindAgent,
		agentID,
	)
	var resultCount int
	var resultJSON, resultOutcome string
	if err := env.db.QueryRow(ctx, `
SELECT count(DISTINCT result.id),
       coalesce(min(block.structured_data::text), ''),
       coalesce(min(result.outcome), '')
FROM tool_call_read_projection tool_call
JOIN tool_call_results result
  ON result.agent_id = tool_call.agent_id
 AND result.tool_call_id = tool_call.id
JOIN content_blocks block
  ON block.agent_id = result.agent_id
 AND block.owner_tool_call_result_id = result.id
 AND block.block_kind = 'structured_data'
WHERE tool_call.project_id = $1
  AND tool_call.agent_id = $2
  AND tool_call.provider_call_id = $3
`, projectUUID, agentUUID, providerCallID).
		Scan(&resultCount, &resultJSON, &resultOutcome); err != nil {
		t.Fatal(err)
	}
	if resultCount != 1 {
		t.Fatalf(
			"tool call %q result rows = %d, want 1",
			providerCallID,
			resultCount,
		)
	}
	wantOutcome := "failed"
	if wantState == "exited" {
		wantOutcome = "succeeded"
	}
	if resultOutcome != wantOutcome {
		t.Fatalf(
			"tool call %q outcome = %q, want %q",
			providerCallID,
			resultOutcome,
			wantOutcome,
		)
	}
	if err := validateServiceE2EProcessToolResult(
		fmt.Sprintf("{\"outcome\":%q}\n%s", resultOutcome, resultJSON),
		wantState,
	); err != nil {
		t.Fatalf(
			"tool call %q result is invalid: %v; result=%s",
			providerCallID,
			err,
			resultJSON,
		)
	}
}

func waitForServiceE2ECanceledToolCall(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, providerCallID string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindProject,
		projectID,
	)
	agentUUID := mustDecodeServiceE2EPublicID(
		t,
		publicid.KindAgent,
		agentID,
	)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var state, outcome string
		var results, events int
		err := env.db.QueryRow(ctx, `
SELECT tool_call.state,
       coalesce(result.outcome, ''),
       count(DISTINCT result.id),
       count(DISTINCT event.sequence)
FROM tool_call_read_projection tool_call
LEFT JOIN tool_call_results result
  ON result.agent_id = tool_call.agent_id
 AND result.tool_call_id = tool_call.id
LEFT JOIN agent_events event
  ON event.agent_id = result.agent_id
 AND event.tool_call_result_id = result.id
 AND event.event_kind = 'tool_result'
WHERE tool_call.project_id = $1
  AND tool_call.agent_id = $2
  AND tool_call.provider_call_id = $3
GROUP BY tool_call.state, result.outcome
`, projectUUID, agentUUID, providerCallID).
			Scan(&state, &outcome, &results, &events)
		if err != nil {
			return false, err.Error()
		}
		if state == "completed" && outcome == "canceled" &&
			results == 1 && events == 1 {
			return true, ""
		}
		return false, fmt.Sprintf(
			"tool call state=%q outcome=%q results=%d events=%d, want completed/canceled/1/1",
			state,
			outcome,
			results,
			events,
		)
	})
}

func readServiceE2EPID(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read process PID %s: %v", path, err)
	}
	pid := strings.TrimSpace(string(body))
	if matched, err := regexp.MatchString(`^[1-9][0-9]*$`, pid); err != nil {
		t.Fatal(err)
	} else if !matched {
		t.Fatalf("process PID %q from %s is not numeric", pid, path)
	}
	return pid
}

type serviceBYOMachine struct {
	machineID   string
	machineName string
	daemonToken string
	workdir     string
}

func (p deterministicProject) bootstrapDockerMachine(t *testing.T, ctx context.Context, seed string) serviceBYOMachine {
	t.Helper()
	machineName := seed + " machine"
	machine := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+p.orgID+"/machines",
		map[string]any{"display_name": machineName},
		"idem-"+seed+"-machine",
		p.adminToken,
		http.StatusCreated,
	)
	machineID := machine["id"].(string)
	tokenResponse := p.env.requestBrowserJSON(
		t,
		ctx,
		http.MethodPost,
		"/api/v1/orgs/"+p.orgID+"/machines/"+machineID+"/daemon-tokens",
		map[string]any{"name": seed + " daemon"},
		"",
		p.adminSession,
		p.adminCSRF,
		http.StatusCreated,
	)
	token := tokenResponse["token"].(string)
	p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/machine-grants",
		map[string]any{"machine_id": machineID},
		"idem-"+seed+"-machine-grant",
		p.adminToken,
		http.StatusCreated,
	)
	workdir := filepath.Join(p.env.root, "machine-"+sanitizeDockerName(seed))
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatalf("create docker machine workdir: %v", err)
	}
	return serviceBYOMachine{
		machineID:   machineID,
		machineName: machineName,
		daemonToken: token,
		workdir:     workdir,
	}
}

func (p *deterministicProject) updateAgentProfileConfigWithMachine(
	t *testing.T,
	ctx context.Context,
	seed string,
	providerConfig string,
	configuredModelName string,
	machineName string,
	defaultCwd string,
	machineSourceFields []string,
	toolsRequiringApproval map[string]struct{},
	tools ...string,
) {
	t.Helper()
	lines := []string{
		"name: Deterministic Service E2E",
		"instruction: Help the user make progress.",
		"model:",
		"  provider_config: " + providerConfig,
		"  name: " + configuredModelName,
		"machine_sources:",
		"  - machine_name: " + machineName,
		"    cwd: " + defaultCwd,
	}
	lines = append(lines, machineSourceFields...)
	if len(tools) > 0 {
		lines = append(lines, "tools:")
		for _, name := range tools {
			if _, requiresApproval := toolsRequiringApproval[name]; requiresApproval {
				lines = append(lines,
					"  "+name+":",
					"    permission:",
					"      mode: always_ask",
				)
				continue
			}
			lines = append(lines, "  "+name+": {}")
		}
	}
	sourceYAML := strings.Join(lines, "\n") + "\n"
	sum := sha256.Sum256([]byte(sourceYAML))
	config := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": sourceYAML},
		"",
		p.adminToken,
		http.StatusCreated,
	)
	updated := p.env.requestJSON(
		t,
		ctx,
		http.MethodPost,
		p.projectPath+"/agent-profiles/"+p.agentID+"/config",
		map[string]any{
			"config":                     config["id"].(string),
			"expected_current_config_id": p.configID,
		},
		"idem-"+seed+"-config-"+hex.EncodeToString(sum[:8]),
		p.adminToken,
		http.StatusOK,
	)
	p.configID = updated["current_config"].(map[string]any)["id"].(string)
}

func (p deterministicProject) startPermissionAutoApprover(t *testing.T, ctx context.Context, agentID string) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		allowed := map[string]bool{}
		ticker := time.NewTicker(50 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			items, ok := p.listOpenInteractions(ctx, agentID)
			if !ok {
				continue
			}
			for _, raw := range items {
				item, ok := raw.(map[string]any)
				if !ok || item["interaction_kind"] != "permission" {
					continue
				}
				id, _ := item["id"].(string)
				if id == "" || allowed[id] {
					continue
				}
				if p.resolvePermissionInteraction(ctx, agentID, id) {
					t.Logf("auto-allowed permission interaction %s", id)
					allowed[id] = true
				}
			}
		}
	}()
	t.Cleanup(func() {
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
		}
	})
}

func (p deterministicProject) listOpenInteractions(ctx context.Context, agentID string) ([]any, bool) {
	req, err := p.env.newAPIRequest(ctx, http.MethodGet, p.projectPath+"/agents/"+agentID+"/interactions?state=open", nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+p.adminToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil, false
	}
	var listed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&listed); err != nil {
		return nil, false
	}
	items, ok := listed["data"].([]any)
	return items, ok
}

func (p deterministicProject) resolvePermissionInteraction(ctx context.Context, agentID, interactionID string) bool {
	body := bytes.NewReader(mustJSON(map[string]any{
		"answers": []map[string]any{{
			"option_indices": []int{toolpermission.AllowOptionIndex},
		}},
	}))
	req, err := p.env.newAPIRequest(
		ctx,
		http.MethodPost,
		p.projectPath+"/agents/"+agentID+"/interactions/"+interactionID+"/resolve",
		body,
	)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+p.adminToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode == http.StatusOK
}

func waitForDaemonRuntime(t *testing.T, ctx context.Context, env *serviceE2EEnvironment, orgID, machineID string) {
	t.Helper()
	orgUUID := mustDecodeServiceE2EPublicID(t, publicid.KindOrganization, orgID)
	machineUUID := mustDecodeServiceE2EPublicID(t, publicid.KindMachine, machineID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM daemon_runtimes WHERE org_id = $1 AND machine_id = $2 AND state = 'active'`, orgUUID, machineUUID).
			Scan(&count)
		if err != nil {
			return false, err.Error()
		}
		return count == 1, "daemon runtime not active yet"
	})
}

func waitForDockerMachineOnline(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	orgID, machineID string,
) {
	t.Helper()
	orgUUID := mustDecodeServiceE2EPublicID(t, publicid.KindOrganization, orgID)
	machineUUID := mustDecodeServiceE2EPublicID(t, publicid.KindMachine, machineID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var lifecycleState, connectionState string
		err := env.db.QueryRow(ctx, `
			SELECT machine.lifecycle_state,
			       CASE WHEN EXISTS (
			         SELECT 1 FROM online_daemon_runtimes runtime
			         WHERE runtime.org_id = machine.org_id AND runtime.machine_id = machine.id
			       ) THEN 'online' ELSE 'offline' END AS connection_state
			FROM machines machine
			WHERE machine.org_id = $1 AND machine.id = $2
		`, orgUUID, machineUUID).Scan(&lifecycleState, &connectionState)
		if err != nil {
			return false, err.Error()
		}
		return lifecycleState == "active" && connectionState == "online", "machine is not online yet"
	})
}

func assertDockerDaemonProcessEvidence(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, machineID, workdir, quickCommandOutput, missingCwd string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	machineUUID := mustDecodeServiceE2EPublicID(t, publicid.KindMachine, machineID)
	quickCommandPath := filepath.Join(workdir, "quick-command.txt")
	data, err := os.ReadFile(quickCommandPath)
	if err != nil {
		t.Fatalf("read quick-command file from Docker workdir: %v", err)
	}
	if strings.TrimSpace(string(data)) != quickCommandOutput {
		t.Fatalf("quick-command file content = %q, want %q", string(data), quickCommandOutput)
	}
	if _, err := os.Stat(filepath.Join(workdir, "stdin-file.txt")); !os.IsNotExist(err) {
		t.Fatalf("stdin-created file should have been deleted, stat err=%v", err)
	}
	interruptMarker, err := os.ReadFile(
		filepath.Join(workdir, "interrupted.txt"),
	)
	if err != nil {
		t.Fatalf("read interrupt marker from Docker workdir: %v", err)
	}
	if string(interruptMarker) != "interrupted" {
		t.Fatalf("interrupt marker = %q, want interrupted", interruptMarker)
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var processCount, terminal int
		if err := env.db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE state IN ('exited','failed','killed','unknown')) FROM processes WHERE project_id = $1 AND agent_id = $2 AND machine_id = $3 AND execution_granted_at IS NOT NULL`, projectUUID, agentUUID, machineUUID).
			Scan(&processCount, &terminal); err != nil {
			return false, err.Error()
		}
		return processCount == 5 && terminal == 5, fmt.Sprintf(
			"process evidence processes=%d terminal=%d, want exactly 5/5",
			processCount,
			terminal,
		)
	})
	var (
		failedState           string
		failedReasonCode      string
		failedReason          string
		failedSourceStartedAt *time.Time
		failedGrantedAt       *time.Time
	)
	if err := env.db.QueryRow(ctx, `
SELECT state,
       state_reason_code,
       state_reason_message,
       source_started_at,
       execution_granted_at
FROM processes
WHERE project_id = $1
  AND agent_id = $2
  AND machine_id = $3
  AND cwd = $4
`, projectUUID, agentUUID, machineUUID, missingCwd).Scan(
		&failedState,
		&failedReasonCode,
		&failedReason,
		&failedSourceStartedAt,
		&failedGrantedAt,
	); err != nil {
		t.Fatalf("query invalid-command process evidence: %v", err)
	}
	if failedState != "failed" ||
		failedReasonCode != "start_failed" ||
		!strings.Contains(failedReason, missingCwd) ||
		failedSourceStartedAt != nil ||
		failedGrantedAt == nil {
		t.Fatalf(
			"invalid-command process evidence state=%q reason_code=%q reason=%q source_started_at=%v granted_at=%v",
			failedState,
			failedReasonCode,
			failedReason,
			failedSourceStartedAt,
			failedGrantedAt,
		)
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var missing []string
		for _, kind := range []string{"write", "interrupt", "terminate", "read"} {
			var count int
			err := env.db.QueryRow(ctx, `SELECT count(*) FROM process_actions WHERE project_id = $1 AND agent_id = $2 AND action_kind = $3 AND state = 'applied'`, projectUUID, agentUUID, kind).
				Scan(&count)
			if err != nil {
				return false, fmt.Sprintf("query process action %s evidence: %v", kind, err)
			}
			if count == 0 {
				missing = append(missing, kind)
			}
		}
		if len(missing) > 0 {
			return false, "missing applied process actions: " + strings.Join(
				missing,
				",",
			) + "; actions=" + processActionEvidence(
				t,
				ctx,
				env,
				projectUUID,
				agentUUID,
			)
		}
		return true, ""
	})
	for _, name := range processToolNames {
		var count int
		err := env.db.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection call
WHERE call.project_id = $1
  AND call.agent_id = $2
  AND call.name = $3
  AND call.state = 'completed'
`, projectUUID, agentUUID, name).
			Scan(&count)
		if err != nil {
			t.Fatalf("query tool call %s evidence: %v", name, err)
		}
		if count == 0 {
			t.Fatalf("no completed tool call recorded for %s", name)
		}
		var authorityCount int
		err = env.db.QueryRow(ctx, `
SELECT count(*)
FROM tool_call_read_projection tc
JOIN tool_call_results result ON result.agent_id = tc.agent_id
  AND result.tool_call_id = tc.id
JOIN agent_events event ON event.agent_id = result.agent_id
  AND event.tool_call_result_id = result.id
  AND event.event_kind = 'tool_result'
WHERE tc.project_id = $1
  AND tc.agent_id = $2
  AND tc.name = $3
  AND tc.state = 'completed'
`, projectUUID, agentUUID, name).Scan(&authorityCount)
		if err != nil {
			t.Fatalf("query typed tool result authority for %s: %v", name, err)
		}
		if authorityCount != count {
			t.Fatalf("typed tool result authority for %s = %d, completed tool calls = %d", name, authorityCount, count)
		}
	}
	expectedApprovals := map[string]int{
		"run_command":    5,
		"write_process":  3,
		"read_process":   0,
		"stop_process":   2,
		"list_processes": 0,
	}
	for name, want := range expectedApprovals {
		var got int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_interaction_read_projection si JOIN tool_call_read_projection tc ON tc.agent_id = si.agent_id AND tc.id = si.tool_call_id WHERE si.project_id = $1 AND si.agent_id = $2 AND si.interaction_kind = 'permission' AND si.state = 'resolved' AND si.resolution->'answers'->0->'option_indices'->>0 = '0' AND tc.name = $3`, projectUUID, agentUUID, name).
			Scan(&got)
		if err != nil {
			t.Fatalf("query permission approval evidence for %s: %v", name, err)
		}
		if got != want {
			t.Fatalf("allowed permission interactions for %s = %d, want %d", name, got, want)
		}
	}
	var resolved int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_inputs WHERE project_id = $1 AND agent_id = $2 AND state = 'resolved'`, projectUUID, agentUUID).
		Scan(&resolved); err != nil {
		t.Fatalf("query resolved inputs: %v", err)
	}
	if resolved == 0 {
		t.Fatalf("expected at least one resolved agent input")
	}
}

func processActionEvidence(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectUUID, agentUUID string,
) string {
	t.Helper()
	var actions string
	err := env.db.QueryRow(ctx, `
SELECT coalesce(
  string_agg(action_kind || ':' || state || ':reason=' || coalesce(state_reason_code, ''), ',' ORDER BY created_at, id),
  ''
)
FROM process_actions
WHERE project_id = $1
  AND agent_id = $2
`, projectUUID, agentUUID).Scan(&actions)
	if err != nil {
		return "query failed: " + err.Error()
	}
	return actions
}

func mustDecodeServiceE2EPublicID(t *testing.T, kind publicid.Kind, value string) string {
	t.Helper()
	id, err := publicid.Decode(kind, value)
	if err != nil {
		t.Fatalf("decode %s public id %q: %v", kind, value, err)
	}
	return id.String()
}

func mustDecodeServiceE2EPublicIDForModel(
	fail fakeModelFailureFunc,
	w http.ResponseWriter,
	kind publicid.Kind,
	value string,
) string {
	id, err := publicid.Decode(kind, value)
	if err != nil {
		fail(w, http.StatusBadRequest, "decode %s public id %q: %v", kind, value, err)
		return ""
	}
	return id.String()
}

func currentProcessID(mu *sync.Mutex, processID *string) string {
	mu.Lock()
	defer mu.Unlock()
	return *processID
}

type fakeModelFailureFunc func(http.ResponseWriter, int, string, ...any)

func fakeModelRequestContainsTools(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	names ...string,
) bool {
	for _, name := range names {
		if !requestContainsTool(body, name) {
			fail(w, http.StatusBadRequest, "request did not expose tool %s: %s", name, mustJSONString(body))
			return false
		}
	}
	return true
}

func fakeModelToolOutputContains(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	callID, contains string,
) bool {
	if !requestContainsToolResult(body, callID, contains) {
		fail(
			w,
			http.StatusBadRequest,
			"request did not include tool result %s containing %q: %s",
			callID,
			contains,
			mustJSONString(body),
		)
		return false
	}
	return true
}

func fakeModelProcessToolResult(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	callID, wantState string,
) bool {
	output := toolResultOutputForCall(body, callID)
	if output == "" {
		fail(
			w,
			http.StatusBadRequest,
			"request did not include tool result %s: %s",
			callID,
			mustJSONString(body),
		)
		return false
	}
	if err := validateServiceE2EProcessToolResult(
		output,
		wantState,
	); err != nil {
		fail(
			w,
			http.StatusBadRequest,
			"tool result %s is invalid: %v; body=%s",
			callID,
			err,
			mustJSONString(body),
		)
		return false
	}
	return true
}

func fakeModelProcessResult(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	callID string,
) (serviceE2EProcessResult, bool) {
	result, err := decodeServiceE2EProcessResult(toolResultOutputForCall(body, callID))
	if err != nil {
		fail(
			w,
			http.StatusBadRequest,
			"tool result %s is not a process result: %v; body=%s",
			callID,
			err,
			mustJSONString(body),
		)
		return serviceE2EProcessResult{}, false
	}
	return result, true
}

func validateServiceE2EProcessToolResult(
	raw string,
	wantState string,
) error {
	result, err := decodeServiceE2EProcessResult(raw)
	if err != nil {
		return err
	}
	if result.State != wantState {
		return fmt.Errorf("state=%q, want %q", result.State, wantState)
	}
	switch wantState {
	case "exited":
		if result.Outcome != "succeeded" {
			return fmt.Errorf("outcome=%q, want succeeded", result.Outcome)
		}
		if result.Done == nil || !*result.Done {
			return errors.New("done is not true")
		}
		if result.ExitCode == nil || *result.ExitCode != 0 {
			return fmt.Errorf("exit_code=%v, want 0", result.ExitCode)
		}
	case "unknown":
		if result.Outcome != "failed" {
			return fmt.Errorf("outcome=%q, want failed", result.Outcome)
		}
	case "failed":
		if result.Outcome != "failed" {
			return fmt.Errorf("outcome=%q, want failed", result.Outcome)
		}
		if result.Done == nil || !*result.Done {
			return errors.New("done is not true")
		}
	default:
		return fmt.Errorf("unsupported expected process state %q", wantState)
	}
	return nil
}

type serviceE2EProcessResult struct {
	Outcome    string `json:"outcome"`
	ProcessID  string `json:"process_id"`
	State      string `json:"state"`
	Output     string `json:"output"`
	Cursor     *int64 `json:"cursor"`
	NextCursor *int64 `json:"next_cursor"`
	Truncated  *bool  `json:"truncated"`
	Done       *bool  `json:"done"`
	ExitCode   *int   `json:"exit_code"`
}

func decodeServiceE2EProcessResult(raw string) (serviceE2EProcessResult, error) {
	if raw == "" {
		return serviceE2EProcessResult{}, errors.New("process result is absent")
	}
	var result serviceE2EProcessResult
	decoder := json.NewDecoder(strings.NewReader(raw))
	for {
		err := decoder.Decode(&result)
		if err == io.EOF {
			return result, nil
		}
		if err != nil {
			return serviceE2EProcessResult{}, fmt.Errorf(
				"decode structured process result: %w",
				err,
			)
		}
	}
}

func fakeModelProcessActionAccepted(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	callID string,
) bool {
	result, ok := fakeModelProcessResult(w, body, fail, callID)
	if !ok {
		return false
	}
	if result.Outcome != "succeeded" || result.State != "applied" {
		fail(
			w,
			http.StatusBadRequest,
			"tool result %s = outcome:%q state:%q, want outcome:succeeded state:applied body=%s",
			callID,
			result.Outcome,
			result.State,
			mustJSONString(body),
		)
		return false
	}
	return true
}

func fakeModelToolProcessID(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	callID string,
) (string, bool) {
	result, ok := fakeModelProcessResult(w, body, fail, callID)
	if !ok {
		return "", false
	}
	if result.ProcessID == "" {
		fail(w, http.StatusBadRequest, "tool result %s did not include process_id: %s", callID, mustJSONString(body))
		return "", false
	}
	return result.ProcessID, true
}

func fakeModelWaitForProcessGrantedToDaemon(
	ctx context.Context,
	w http.ResponseWriter,
	env *serviceE2EEnvironment,
	fail fakeModelFailureFunc,
	projectID, agentID, processID string,
) bool {
	projectUUID := mustDecodeServiceE2EPublicIDForModel(fail, w, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicIDForModel(fail, w, publicid.KindAgent, agentID)
	processUUID := mustDecodeServiceE2EPublicIDForModel(fail, w, publicid.KindProcess, processID)
	if projectUUID == "" || agentUUID == "" || processUUID == "" {
		return false
	}
	deadline := time.Now().Add(10 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			fail(
				w,
				http.StatusGatewayTimeout,
				"context canceled while waiting for process %s daemon assignment: %v",
				processID,
				ctx.Err(),
			)
			return false
		default:
		}
		var state string
		err := env.db.QueryRow(ctx, `SELECT state FROM processes WHERE project_id = $1 AND agent_id = $2 AND id = $3`, projectUUID, agentUUID, processUUID).
			Scan(&state)
		if err != nil {
			last = err.Error()
		} else if state == "starting" || state == "running" {
			return true
		} else {
			last = "process execution has not been granted yet"
		}
		select {
		case <-ctx.Done():
			fail(
				w,
				http.StatusGatewayTimeout,
				"context canceled while waiting for process %s daemon assignment: %v",
				processID,
				ctx.Err(),
			)
			return false
		case <-time.After(25 * time.Millisecond):
		}
	}
	fail(w, http.StatusGatewayTimeout, "process %s was not granted to the machine daemon: %s", processID, last)
	return false
}

func toolResultOutputForCall(body map[string]any, callID string) string {
	input, ok := body["input"].([]any)
	if !ok {
		return ""
	}
	for _, raw := range input {
		item, ok := raw.(map[string]any)
		if !ok || item["type"] != "function_call_output" || item["call_id"] != callID {
			continue
		}
		switch output := item["output"].(type) {
		case string:
			return output
		case []any:
			var b strings.Builder
			for _, partRaw := range output {
				part, ok := partRaw.(map[string]any)
				if !ok || part["type"] != "input_text" {
					continue
				}
				text, _ := part["text"].(string)
				if text == "" {
					continue
				}
				if b.Len() > 0 {
					b.WriteByte('\n')
				}
				b.WriteString(text)
			}
			return b.String()
		default:
			return mustJSONString(output)
		}
	}
	return ""
}

func writeOpenAIFunctionCall(
	w http.ResponseWriter,
	fail fakeModelFailureFunc,
	responseID, callID, name string,
	args map[string]any,
) {
	writeOpenAIFunctionCalls(w, fail, responseID, fakeOpenAIFunctionCall{
		CallID: callID,
		Name:   name,
		Args:   args,
	})
}

type fakeOpenAIFunctionCall struct {
	CallID string
	Name   string
	Args   map[string]any
}

func writeOpenAIFunctionCalls(
	w http.ResponseWriter,
	fail fakeModelFailureFunc,
	responseID string,
	calls ...fakeOpenAIFunctionCall,
) {
	output := make([]map[string]any, 0, len(calls))
	for _, call := range calls {
		arguments, err := json.Marshal(call.Args)
		if err != nil {
			fail(w, http.StatusInternalServerError, "marshal function args: %v", err)
			return
		}
		output = append(output, map[string]any{
			"type":      "function_call",
			"call_id":   call.CallID,
			"name":      call.Name,
			"arguments": string(arguments),
		})
	}
	body, err := json.Marshal(map[string]any{
		"id":     responseID,
		"status": "completed",
		"output": output,
		"usage":  map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "marshal OpenAI response: %v", err)
		return
	}
	_, _ = w.Write(body)
}

func writeOpenAIMessage(w http.ResponseWriter, fail fakeModelFailureFunc, responseID, text string) {
	body, err := json.Marshal(map[string]any{
		"id":     responseID,
		"status": "completed",
		"output": []map[string]any{{
			"id":   responseID + "_message",
			"type": "message",
			"content": []map[string]any{{
				"type": "output_text",
				"text": text,
			}},
		}},
		"usage": map[string]any{"input_tokens": 10, "output_tokens": 5},
	})
	if err != nil {
		fail(w, http.StatusInternalServerError, "marshal OpenAI message response: %v", err)
		return
	}
	_, _ = w.Write(body)
}
