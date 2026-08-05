//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
)

// Keep these markers to one byte: wait_ms returns on the first nonempty output
// snapshot, which need not contain an entire multi-byte message.
const (
	parallelServiceE2EReadyMarker    = "R"
	parallelServiceE2EReleasedMarker = "G"
)

func TestServiceE2EParallelProcessActionsRecoverFIFOAfterDaemonRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()

	const (
		seed         = "parallel-process-daemon-restart"
		processCount = 3
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
		"write_process",
		"read_process",
		"stop_process",
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
		nil,
		"run_command",
		"write_process",
		"read_process",
		"stop_process",
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
	actionBatchIssued := make(chan []string, 1)
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
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			failModelRequest(w, http.StatusBadRequest, "decode OpenAI request: %v", err)
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
				"write_process",
				"read_process",
				"stop_process",
			) {
				return
			}
			calls := make([]fakeOpenAIFunctionCall, 0, processCount)
			for i := range processCount {
				calls = append(calls, fakeOpenAIFunctionCall{
					CallID: fmt.Sprintf("call_parallel_run_%d", i),
					Name:   "run_command",
					Args: map[string]any{
						"command": parallelServiceE2ECommand(i),
						"shell":   "sh",
						"cwd":     "/work",
						"wait_ms": 10000,
					},
				})
			}
			writeOpenAIFunctionCalls(w, failModelRequest, "resp_parallel_runs", calls...)
		case 2:
			ids := make([]string, processCount)
			for i := range processCount {
				callID := fmt.Sprintf("call_parallel_run_%d", i)
				result, ok := expectServiceE2EProcessResult(
					w,
					body,
					failModelRequest,
					callID,
					"running",
					parallelServiceE2EReadyMarker,
				)
				if !ok {
					return
				}
				ids[i] = result.ProcessID
			}
			calls := make([]fakeOpenAIFunctionCall, 0, 7)
			for i, processID := range ids {
				calls = append(calls, serviceE2EReadProcessCall(
					fmt.Sprintf("call_parallel_gate_%d", i),
					processID,
					10000,
				))
			}
			for _, write := range []struct {
				process int
				data    string
			}{{0, "A\n"}, {1, "A\n"}, {0, "B\n"}, {2, "A\n"}} {
				calls = append(calls, serviceE2EWriteProcessCall(
					fmt.Sprintf(
						"call_parallel_write_%d_%s",
						write.process,
						strings.TrimSpace(write.data),
					),
					ids[write.process],
					write.data,
					false,
				))
			}
			writeOpenAIFunctionCalls(w, failModelRequest, "resp_parallel_blocked_actions", calls...)
			actionBatchIssued <- ids
		case 3:
			ids := make([]string, processCount)
			for i := range processCount {
				result, ok := expectServiceE2EProcessResult(
					w,
					body,
					failModelRequest,
					fmt.Sprintf("call_parallel_gate_%d", i),
					"running",
					parallelServiceE2EReleasedMarker,
				)
				if !ok {
					return
				}
				ids[i] = result.ProcessID
			}
			for _, callID := range []string{
				"call_parallel_write_0_A",
				"call_parallel_write_1_A",
				"call_parallel_write_0_B",
				"call_parallel_write_2_A",
			} {
				if !fakeModelProcessActionAccepted(w, body, failModelRequest, callID) {
					return
				}
			}
			writeOpenAIFunctionCalls(
				w,
				failModelRequest,
				"resp_parallel_observe_and_close",
				serviceE2EReadProcessCall("call_parallel_observe_0", ids[0], 0),
				serviceE2EReadProcessCall("call_parallel_observe_1", ids[1], 0),
				serviceE2EReadProcessCall("call_parallel_observe_2", ids[2], 0),
				serviceE2EWriteProcessCall("call_parallel_close_0", ids[0], "", true),
				serviceE2EWriteProcessCall("call_parallel_close_1", ids[1], "", true),
				serviceE2EStopProcessCall("call_parallel_interrupt_2", ids[2], "interrupt"),
			)
		case 4:
			ids := make([]string, processCount)
			for i := range processCount {
				result, ok := expectServiceE2EProcessResult(
					w,
					body,
					failModelRequest,
					fmt.Sprintf("call_parallel_observe_%d", i),
					"running",
					"",
				)
				if !ok {
					return
				}
				ids[i] = result.ProcessID
			}
			for _, callID := range []string{
				"call_parallel_close_0",
				"call_parallel_close_1",
				"call_parallel_interrupt_2",
			} {
				if !fakeModelProcessActionAccepted(w, body, failModelRequest, callID) {
					return
				}
			}
			calls := make([]fakeOpenAIFunctionCall, 0, processCount)
			for i, processID := range ids {
				calls = append(calls, serviceE2EReadProcessCall(
					fmt.Sprintf("call_parallel_terminal_%d", i),
					processID,
					10000,
				))
			}
			writeOpenAIFunctionCalls(w, failModelRequest, "resp_parallel_terminal_reads", calls...)
		case 5:
			for i := range processCount {
				result, ok := expectServiceE2EProcessResult(
					w,
					body,
					failModelRequest,
					fmt.Sprintf("call_parallel_terminal_%d", i),
					"",
					"",
				)
				if !ok {
					return
				}
				if result.Done == nil || !*result.Done {
					failModelRequest(
						w,
						http.StatusBadRequest,
						"terminal read %d was not done: %+v",
						i,
						result,
					)
					return
				}
			}
			writeOpenAIMessage(
				w,
				failModelRequest,
				"resp_parallel_done",
				"PARALLEL_DAEMON_RESTART_DONE",
			)
		default:
			failModelRequest(
				w,
				http.StatusTeapot,
				"unexpected OpenAI request %d: %s",
				step,
				mustJSONString(body),
			)
		}
	}))
	defer openai.Close()

	project.createInput(t, ctx, agentID, "control three parallel processes across a daemon restart")
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        openai.URL,
	})

	var ids []string
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	case processIDs := <-actionBatchIssued:
		ids = processIDs
	}
	_, priorRuntimeID := waitForServiceE2EGrantedProcess(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
	)
	waitForServiceE2EParallelActionBarrier(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		ids,
	)
	daemon.crashManagedDaemon(t, ctx)
	waitForReplacementServiceE2EDaemonRuntime(
		t,
		ctx,
		env,
		project.orgID,
		machine.machineID,
		priorRuntimeID,
	)
	waitForServiceE2EParallelActionBarrier(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		ids,
	)
	for i := range processCount {
		releasePath := filepath.Join(machine.workdir, fmt.Sprintf("parallel-release-%d", i))
		if err := os.WriteFile(releasePath, []byte("release"), 0o600); err != nil {
			t.Fatalf("release process %d: %v", i, err)
		}
	}
	waitForServiceE2ERecoveryAssistantText(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		"PARALLEL_DAEMON_RESTART_DONE",
		modelFailures,
		requestCount.Load,
		api,
		worker,
		daemon,
	)
	assertServiceE2EParallelJourney(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		ids,
		machine.workdir,
	)
	if got := requestCount.Load(); got != 5 {
		t.Fatalf("deterministic OpenAI server saw %d requests, want 5", got)
	}
	select {
	case failure := <-modelFailures:
		t.Fatalf("deterministic fake model assertion failed: %s", failure)
	default:
	}
}

func parallelServiceE2ECommand(index int) string {
	return fmt.Sprintf(
		"printf x >> /work/parallel-start-%[1]d.txt; "+
			"printf '"+parallelServiceE2EReadyMarker+"'; "+
			"while [ ! -f /work/parallel-release-%[1]d ]; do sleep 0.05; done; "+
			"printf '"+parallelServiceE2EReleasedMarker+"'; "+
			"trap 'exit 130' INT; "+
			"while IFS= read -r line; do "+
			"printf '%%s' \"$line\" >> /work/parallel-input-%[1]d.txt; "+
			"done",
		index,
	)
}

func serviceE2EReadProcessCall(
	callID, processID string,
	waitMS int,
) fakeOpenAIFunctionCall {
	args := map[string]any{
		"process_id": processID,
		"max_bytes":  4096,
	}
	if waitMS > 0 {
		args["wait_ms"] = waitMS
	}
	return fakeOpenAIFunctionCall{
		CallID: callID,
		Name:   "read_process",
		Args:   args,
	}
}

func serviceE2EWriteProcessCall(
	callID, processID, data string,
	closeStdin bool,
) fakeOpenAIFunctionCall {
	args := map[string]any{"process_id": processID}
	if data != "" {
		args["data"] = data
	}
	if closeStdin {
		args["close_stdin"] = true
	}
	return fakeOpenAIFunctionCall{CallID: callID, Name: "write_process", Args: args}
}

func serviceE2EStopProcessCall(
	callID, processID, mode string,
) fakeOpenAIFunctionCall {
	return fakeOpenAIFunctionCall{
		CallID: callID,
		Name:   "stop_process",
		Args: map[string]any{
			"process_id": processID,
			"mode":       mode,
		},
	}
}

func expectServiceE2EProcessResult(
	w http.ResponseWriter,
	body map[string]any,
	fail fakeModelFailureFunc,
	callID, wantState, wantOutput string,
) (serviceE2EProcessResult, bool) {
	result, ok := fakeModelProcessResult(w, body, fail, callID)
	if !ok {
		return serviceE2EProcessResult{}, false
	}
	if (wantState != "" && result.State != wantState) ||
		(wantOutput != "" && !strings.Contains(result.Output, wantOutput)) ||
		result.ProcessID == "" {
		fail(
			w,
			http.StatusBadRequest,
			"process result %s = %+v, want state=%q output containing %q",
			callID,
			result,
			wantState,
			wantOutput,
		)
		return serviceE2EProcessResult{}, false
	}
	return result, true
}

func serviceE2EProcessUUIDs(t *testing.T, processIDs []string) []uuid.UUID {
	t.Helper()
	ids := make([]uuid.UUID, 0, len(processIDs))
	for _, processID := range processIDs {
		id, err := publicid.Decode(publicid.KindProcess, processID)
		if err != nil {
			t.Fatalf("decode process ID %q: %v", processID, err)
		}
		ids = append(ids, id)
	}
	return ids
}

func waitForServiceE2EParallelActionBarrier(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID string,
	processIDs []string,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	processUUIDs := serviceE2EProcessUUIDs(t, processIDs)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var running, acceptedReads, queuedWrites, totalActions int
		err := env.db.QueryRow(ctx, `
SELECT count(DISTINCT process.id) FILTER (WHERE process.state = 'running'),
       count(*) FILTER (WHERE action.action_kind = 'read' AND action.state = 'accepted'),
       count(*) FILTER (WHERE action.action_kind = 'write' AND action.state = 'queued'),
       count(*)
FROM processes process
JOIN process_actions action
  ON action.project_id = process.project_id
 AND action.agent_id = process.agent_id
 AND action.process_id = process.id
WHERE process.project_id = $1
  AND process.agent_id = $2
  AND process.id = ANY($3::uuid[])
`, projectUUID, agentUUID, processUUIDs).Scan(
			&running,
			&acceptedReads,
			&queuedWrites,
			&totalActions,
		)
		if err != nil {
			return false, err.Error()
		}
		if running == 3 && acceptedReads == 3 && queuedWrites == 4 &&
			totalActions == 7 {
			return true, ""
		}
		return false, fmt.Sprintf(
			"running=%d accepted_reads=%d queued_writes=%d actions=%d, want 3/3/4/7",
			running,
			acceptedReads,
			queuedWrites,
			totalActions,
		)
	})
}

func assertServiceE2EParallelJourney(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID string,
	processIDs []string,
	workdir string,
) {
	t.Helper()
	for i, want := range []string{"AB", "A", "A"} {
		for _, file := range []struct {
			name string
			want string
		}{
			{fmt.Sprintf("parallel-start-%d.txt", i), "x"},
			{fmt.Sprintf("parallel-input-%d.txt", i), want},
		} {
			path := filepath.Join(workdir, file.name)
			waitForServiceE2ECondition(t, ctx, func() (bool, string) {
				body, err := os.ReadFile(path)
				if err == nil && string(body) == file.want {
					return true, ""
				}
				return false, fmt.Sprintf(
					"%s = %q err=%v, want %q",
					path,
					body,
					err,
					file.want,
				)
			})
		}
	}
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	var total, granted int
	if err := env.db.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE execution_granted_at IS NOT NULL)
FROM processes
WHERE project_id = $1 AND agent_id = $2
`, projectUUID, agentUUID).Scan(&total, &granted); err != nil {
		t.Fatal(err)
	}
	if total != 3 || granted != 3 {
		t.Fatalf(
			"processes total=%d granted=%d, want 3/3",
			total,
			granted,
		)
	}
	processUUIDs := serviceE2EProcessUUIDs(t, processIDs)
	for i, want := range []struct {
		state    string
		exitCode int
	}{
		{"exited", 0},
		{"exited", 0},
		{"failed", 130},
	} {
		var state string
		var exitCode int
		if err := env.db.QueryRow(ctx, `
SELECT state, coalesce(exit_code, -1)
FROM processes
WHERE project_id = $1 AND agent_id = $2 AND id = $3
`, projectUUID, agentUUID, processUUIDs[i]).Scan(&state, &exitCode); err != nil {
			t.Fatal(err)
		}
		if state != want.state || exitCode != want.exitCode {
			t.Fatalf(
				"process %d state/exit = %q/%d, want %q/%d",
				i,
				state,
				exitCode,
				want.state,
				want.exitCode,
			)
		}
	}
	var actions, reads, writes, interrupts, applied int
	if err := env.db.QueryRow(ctx, `
SELECT count(*),
       count(*) FILTER (WHERE action_kind = 'read'),
       count(*) FILTER (WHERE action_kind = 'write'),
       count(*) FILTER (WHERE action_kind = 'interrupt'),
       count(*) FILTER (WHERE state = 'applied')
FROM process_actions
WHERE project_id = $1 AND agent_id = $2
`, projectUUID, agentUUID).Scan(
		&actions,
		&reads,
		&writes,
		&interrupts,
		&applied,
	); err != nil {
		t.Fatal(err)
	}
	if actions != 16 || reads != 9 || writes != 6 || interrupts != 1 ||
		applied != actions {
		t.Fatalf(
			"actions total=%d read=%d write=%d interrupt=%d applied=%d",
			actions,
			reads,
			writes,
			interrupts,
			applied,
		)
	}
}
