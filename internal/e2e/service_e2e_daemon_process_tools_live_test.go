//go:build integration && servicee2e && live

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestServiceE2ELiveOpenAIDockerDaemonProcessTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Fatal("OPENAI_API_KEY is required for live OpenAI Docker daemon process E2E")
	}
	runLiveDockerDaemonProcessTools(t, ctx, liveDockerDaemonProcessOptions{
		Seed:                "live-openai-docker-daemon-process-tools",
		ProviderConfig:      "openai-prod",
		ConfiguredModelName: liveOpenAIConfiguredModelName(),
		BaseURL:             os.Getenv("OPENAI_BASE_URL"),
	})
}

func TestServiceE2ELiveAnthropicDockerDaemonProcessTools(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	apiKey := os.Getenv("ANTHROPIC_API_KEY")
	if apiKey == "" {
		t.Fatal("ANTHROPIC_API_KEY is required for live Anthropic Docker daemon process E2E")
	}
	runLiveDockerDaemonProcessTools(t, ctx, liveDockerDaemonProcessOptions{
		Seed:                "live-anthropic-docker-daemon-process-tools",
		ProviderConfig:      "anthropic-prod",
		ConfiguredModelName: liveAnthropicConfiguredModelName(),
		BaseURL:             os.Getenv("ANTHROPIC_BASE_URL"),
	})
}

type liveDockerDaemonProcessOptions struct {
	Seed                string
	ProviderConfig      string
	ConfiguredModelName string
	BaseURL             string
}

func runLiveDockerDaemonProcessTools(t *testing.T, ctx context.Context, opts liveDockerDaemonProcessOptions) {
	t.Helper()
	env := newServiceE2EEnvironment(t, ctx, opts.Seed)
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPIWithTools(
		t,
		ctx,
		opts.Seed,
		opts.ProviderConfig,
		opts.ConfiguredModelName,
		processToolNames...)
	nonce := strings.ToUpper(strings.ReplaceAll(opts.Seed+"-"+env.seed, "-", "_"))
	machine := project.bootstrapDockerMachine(t, ctx, opts.Seed+"-byo-machine")
	project.updateAgentProfileConfigWithMachine(
		t,
		ctx,
		opts.Seed+"-machine",
		opts.ProviderConfig,
		opts.ConfiguredModelName,
		machine.machineName,
		"/work",
		nil,
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
	project.startPermissionAutoApprover(t, ctx, agentID)
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: opts.ProviderConfig,
		BaseURL:        opts.BaseURL,
	})

	project.createInput(t, ctx, agentID, liveDockerDaemonProcessPrompt(nonce))
	waitForLiveAssistantText(t, ctx, env, project.projectID, agentID, "DAEMON_LIVE_DONE_"+nonce, worker, daemon)
	assertLiveDockerDaemonProcessEvidence(
		t,
		ctx,
		env,
		project.projectID,
		agentID,
		machine.machineID,
		machine.workdir,
		nonce,
		daemon,
	)
}

func liveDockerDaemonProcessPrompt(nonce string) string {
	return strings.Join([]string{
		"You must use the daemon-backed process tools, not simulate the result.",
		"Use the assigned machine default cwd; do not use / as cwd.",
		"Use the exact commands below. Do not improvise alternate commands.",
		"Perform these operations in order:",
		"1. Use run_command once with this command: printf '%s\\n' '" + nonce + "' > live-quick-command.txt && cat live-quick-command.txt",
		"2. Use run_command with this command: while IFS= read -r line; do if [ \"$line\" = CREATE ]; then printf '%s\\n' created > live-stdin.txt; printf '%s\\n' CREATED; elif [ \"$line\" = DELETE ]; then rm -f live-stdin.txt; printf '%s\\n' DELETED; fi; done",
		"3. Use write_process to send exactly CREATE plus a newline, then use read_process once to observe CREATED.",
		"4. Use write_process to send exactly DELETE plus a newline, then use read_process once to observe DELETED.",
		"5. Use write_process with close_stdin=true and then read_process with wait_ms until that process is done.",
		"6. Use run_command with this command: trap 'printf \"%s\\n\" INTERRUPTED; exit 130' INT TERM; printf '%s\\n' READY; sleep 20",
		"7. Use read_process to observe READY, then use stop_process with mode=interrupt, then use read_process with wait_ms to observe INTERRUPTED or done.",
		"8. Use run_command with this command: sleep 20",
		"9. Use list_processes once.",
		"10. Use stop_process with mode=terminate on the sleeping process from step 8, then use read_process with wait_ms until it is done.",
		"Only after the tool evidence is complete, reply with exactly DAEMON_LIVE_DONE_" + nonce,
	}, "\n")
}

func assertLiveDockerDaemonProcessEvidence(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, machineID, workdir, nonce string,
	daemon serviceProcess,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	machineUUID := mustDecodeServiceE2EPublicID(t, publicid.KindMachine, machineID)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		data, err := os.ReadFile(filepath.Join(workdir, "live-quick-command.txt"))
		if err != nil {
			return false, err.Error()
		}
		return strings.TrimSpace(string(data)) == nonce, "live quick-command file does not exactly match nonce"
	})
	if _, err := os.Stat(filepath.Join(workdir, "live-stdin.txt")); !os.IsNotExist(err) {
		t.Fatalf("live stdin file should have been deleted, stat err=%v", err)
	}
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
			t.Fatalf("query live tool call %s evidence: %v", name, err)
		}
		if count == 0 {
			t.Fatalf("live model did not complete required tool %s; daemon logs=%s", name, daemon.logExcerpt())
		}
	}
	for _, kind := range []string{"write", "interrupt", "terminate", "read"} {
		var count int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM process_actions WHERE project_id = $1 AND agent_id = $2 AND action_kind = $3 AND state = 'applied'`, projectUUID, agentUUID, kind).
			Scan(&count)
		if err != nil {
			t.Fatalf("query live process action %s evidence: %v", kind, err)
		}
		if count == 0 {
			t.Fatalf("live model did not produce applied process action %s; daemon logs=%s", kind, daemon.logExcerpt())
		}
	}
	var processCount int
	if err := env.db.QueryRow(ctx, `SELECT count(*) FROM processes WHERE project_id = $1 AND agent_id = $2 AND machine_id = $3 AND execution_granted_at IS NOT NULL`, projectUUID, agentUUID, machineUUID).
		Scan(&processCount); err != nil {
		t.Fatalf("query live process evidence: %v", err)
	}
	if processCount < 4 {
		t.Fatalf("live process count = %d, want at least 4; daemon logs=%s", processCount, daemon.logExcerpt())
	}
}

func waitForLiveAssistantText(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectID, agentID, contains string,
	worker, daemon serviceProcess,
) {
	t.Helper()
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	deadline := time.Now().Add(7 * time.Minute)
	waitForServiceE2EConditionUntil(t, ctx, deadline, func() (bool, string) {
		var messages int
		err := env.db.QueryRow(ctx, `SELECT count(*) FROM agent_events event JOIN agents agent ON agent.id = event.agent_id JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text' AND block.text_content LIKE '%' || $3 || '%'`, projectUUID, agentUUID, contains).
			Scan(&messages)
		if err != nil {
			return false, err.Error()
		}
		if messages == 1 {
			return true, ""
		}
		var locks, wakeups, toolCalls, openInteractions, processActions int
		_ = env.db.QueryRow(ctx, scopedAgentRuntimeLockCountSQL, projectUUID, agentUUID).
			Scan(&locks)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2`, projectUUID, agentUUID).
			Scan(&wakeups)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM tool_call_read_projection WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&toolCalls)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM agent_interaction_read_projection WHERE project_id = $1 AND agent_id = $2 AND state = 'open'`, projectUUID, agentUUID).
			Scan(&openInteractions)
		_ = env.db.QueryRow(ctx, `SELECT count(*) FROM process_actions WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID).
			Scan(&processActions)
		return false, "live assistant token missing" +
			" locks=" + itoa(locks) +
			" wakeups=" + itoa(wakeups) +
			" tool_calls=" + itoa(toolCalls) +
			" open_interactions=" + itoa(openInteractions) +
			" process_actions=" + itoa(processActions) +
			" state={" + liveProcessToolsStateSummary(t, ctx, env, projectUUID, agentUUID) + "}" +
			" worker_logs=" + worker.logExcerpt() +
			" daemon_logs=" + daemon.logExcerpt()
	})
}

func liveProcessToolsStateSummary(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	projectUUID, agentUUID string,
) string {
	t.Helper()
	var toolStates, actionStates, processStates, contextStates, recentEvents string
	scanSummary := func(query string, args ...any) string {
		var out string
		if err := env.db.QueryRow(ctx, query, args...).Scan(&out); err != nil {
			return "query_error=" + err.Error()
		}
		if out == "" {
			return "<none>"
		}
		return out
	}
	toolStates = scanSummary(`
SELECT coalesce(string_agg(
  call.name || ':' || call.type || ':' || call.state || ':outcome=' || coalesce(result.outcome, '') || ':provider=' || coalesce(call.provider_call_id, ''),
  ',' ORDER BY call.created_at, call.id
), '')
FROM tool_call_read_projection call
LEFT JOIN tool_call_results result ON result.agent_id = call.agent_id
  AND result.tool_call_id = call.id
WHERE call.project_id = $1 AND call.agent_id = $2`, projectUUID, agentUUID)
	actionStates = scanSummary(`
SELECT coalesce(string_agg(
  action_kind || ':' || state || ':seq=' || seq::text || ':reason=' || coalesce(state_reason_code, ''),
  ',' ORDER BY created_at, id
), '')
FROM process_actions
WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID)
	processStates = scanSummary(`
SELECT coalesce(string_agg(
  state || ':granted=' || (execution_granted_at IS NOT NULL)::text || ':cmd=' || command,
  ',' ORDER BY created_at, id
), '')
FROM processes
WHERE project_id = $1 AND agent_id = $2`, projectUUID, agentUUID)
	contextStates = scanSummary(`
SELECT coalesce(string_agg(
  mcc.state || ':attempt=' || mcc.attempt_number::text || ':api_format=' || mcc.api_format || ':model=' || revision.provider_model_slug || ':err=' || mcc.error_kind,
  ',' ORDER BY mcc.created_at, mcc.id
), '')
FROM model_call_contexts mcc
JOIN configured_model_revisions revision
  ON revision.org_id = mcc.org_id
 AND revision.id = mcc.configured_model_revision_id
WHERE mcc.project_id = $1 AND mcc.agent_id = $2`, projectUUID, agentUUID)
	recentEvents = scanSummary(`
SELECT coalesce(string_agg(event_kind || '#' || sequence::text, ',' ORDER BY sequence), '')
FROM (
  SELECT event_kind, sequence
  FROM agent_events event
  JOIN agents agent ON agent.id = event.agent_id
  WHERE agent.project_id = $1 AND event.agent_id = $2
  ORDER BY sequence DESC
  LIMIT 12
) recent`, projectUUID, agentUUID)
	return fmt.Sprintf(
		"tools=%s actions=%s processes=%s contexts=%s recent_events=%s",
		toolStates,
		actionStates,
		processStates,
		contextStates,
		recentEvents,
	)
}

func itoa(value int) string {
	return strconv.Itoa(value)
}
