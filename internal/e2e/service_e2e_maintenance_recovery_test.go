//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestServiceE2EMaintenanceRecoversCrashedModelCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "maintenance-recovers-crashed-model-call")

	var requestCount atomic.Int64
	firstRequestStarted := make(chan struct{})
	firstRequestStopped := make(chan struct{})
	const recoveredText = "maintenance recovered the crashed model call"
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode maintenance recovery request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		switch requestCount.Add(1) {
		case 1:
			close(firstRequestStarted)
			defer close(firstRequestStopped)
			<-r.Context().Done()
		case 2:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(
				`{"id":"resp_maintenance_recovery","status":"completed","output":[` +
					`{"id":"msg_maintenance_recovery","type":"message","content":[` +
					`{"type":"output_text","text":"` + recoveredText + `"}]}],` +
					`"usage":{"input_tokens":8,"output_tokens":6}}`,
			))
		default:
			t.Errorf("unexpected extra maintenance recovery request: %+v", body)
			http.Error(w, "unexpected request", http.StatusTeapot)
		}
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "maintenance-recovery", "openai-prod", "service-e2e-local")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "recover this turn after a worker crash")
	firstWorker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        openai.URL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	select {
	case <-firstRequestStarted:
	case <-ctx.Done():
		t.Fatalf("first provider request did not start: %v", ctx.Err())
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var startedContexts, locks int
		if err := env.db.QueryRow(ctx, `
SELECT count(*) FILTER (
         WHERE context.operation_kind = 'normal'
           AND context.attempt_number = 1
           AND context.state = 'started'
       ),
       (SELECT count(*)
        FROM agent_runtime_locks runtime_lock
        JOIN agents runtime_agent ON runtime_agent.id = runtime_lock.agent_id
        WHERE runtime_agent.project_id = $1 AND runtime_lock.agent_id = $2)
FROM model_call_contexts context
WHERE context.project_id = $1 AND context.agent_id = $2
`, projectUUID, agentUUID).Scan(&startedContexts, &locks); err != nil {
			return false, err.Error()
		}
		return startedContexts == 1 && locks == 1,
			fmt.Sprintf("started contexts/locks = %d/%d, want 1/1", startedContexts, locks)
	})

	if err := firstWorker.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill first worker during provider request: %v", err)
	}
	_ = firstWorker.cmd.Wait()
	select {
	case <-firstRequestStopped:
	case <-ctx.Done():
		t.Fatalf("crashed worker provider request did not stop: %v", ctx.Err())
	}
	staleAt := time.Now().UTC().Add(-time.Hour)
	if tag, err := env.db.Exec(ctx, `
UPDATE agent_runtime_locks runtime_lock
SET started_at = $3::timestamptz - interval '2 seconds',
    renewed_at = $3::timestamptz - interval '1 second',
    lease_expires_at = $3::timestamptz
FROM agents agent
WHERE agent.id = runtime_lock.agent_id
  AND agent.project_id = $1
  AND runtime_lock.agent_id = $2
`, projectUUID, agentUUID, staleAt); err != nil {
		t.Fatalf("age crashed worker runtime lock: %v", err)
	} else if tag.RowsAffected() != 1 {
		t.Fatalf("aged runtime locks = %d, want 1", tag.RowsAffected())
	}

	maintenance := env.startMaintenance(t, ctx)
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var recoveredContexts, contextRows, locks, wakeups int
		if err := env.db.QueryRow(ctx, `
SELECT count(*) FILTER (
         WHERE operation_kind = 'normal'
           AND attempt_number = 1
           AND state = 'failed'
           AND recovery_kind = 'retry'
           AND error_kind = 'runtime'
           AND error_code = 'runtime_lease_expired_before_model_result_acceptance'
           AND error_details @> '{"outcome_ambiguous":true}'::jsonb
           AND retry_at IS NOT NULL
       ),
       count(*),
       (SELECT count(*)
        FROM agent_runtime_locks runtime_lock
        JOIN agents runtime_agent ON runtime_agent.id = runtime_lock.agent_id
        WHERE runtime_agent.project_id = $1 AND runtime_lock.agent_id = $2),
       (SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2)
FROM model_call_contexts
WHERE project_id = $1 AND agent_id = $2
`, projectUUID, agentUUID).Scan(&recoveredContexts, &contextRows, &locks, &wakeups); err != nil {
			return false, err.Error()
		}
		return recoveredContexts == 1 && contextRows == 1 && locks == 0 && wakeups == 1,
			fmt.Sprintf(
				"maintenance recovery contexts/rows/locks/wakeups = %d/%d/%d/%d, want 1/1/0/1; logs=%s",
				recoveredContexts,
				contextRows,
				locks,
				wakeups,
				maintenance.logExcerpt(),
			)
	})

	secondWorker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        openai.URL,
	})
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var outputs, recoveredContexts, succeededContexts, contextRows int
		var distinctFrontiers, distinctConfigs, firstAttempt, lastAttempt int
		var producerOutputs, locks, wakeups int
		if err := env.db.QueryRow(ctx, `
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
  AND block.text_content = $3
`, projectUUID, agentUUID, recoveredText).Scan(&outputs); err != nil {
			return false, err.Error()
		}
		if err := env.db.QueryRow(ctx, `
SELECT count(*) FILTER (
         WHERE context.attempt_number = 1
           AND context.state = 'failed'
           AND context.recovery_kind = 'retry'
           AND context.error_kind = 'runtime'
           AND context.error_details @> '{"outcome_ambiguous":true}'::jsonb
       ),
       count(*) FILTER (
         WHERE context.attempt_number = 2
           AND context.state = 'succeeded'
       ),
       count(*),
       count(DISTINCT context.input_event_sequence),
       count(DISTINCT context.agent_config_id),
       coalesce(min(context.attempt_number), 0),
       coalesce(max(context.attempt_number), 0),
       (
         SELECT count(*)
         FROM model_outputs output
         JOIN model_call_contexts producer
           ON producer.agent_id = output.agent_id
          AND producer.id = output.model_call_context_id
         WHERE producer.project_id = $1
           AND producer.agent_id = $2
           AND producer.operation_kind = 'normal'
           AND producer.attempt_number = 2
           AND producer.state = 'succeeded'
       ),
       (SELECT count(*)
        FROM agent_runtime_locks runtime_lock
        JOIN agents runtime_agent ON runtime_agent.id = runtime_lock.agent_id
        WHERE runtime_agent.project_id = $1 AND runtime_lock.agent_id = $2),
       (SELECT count(*) FROM agent_wakeups wake JOIN agents agent ON agent.id = wake.agent_id WHERE agent.project_id = $1 AND wake.agent_id = $2)
FROM model_call_contexts context
WHERE context.project_id = $1
  AND context.agent_id = $2
  AND context.operation_kind = 'normal'
`, projectUUID, agentUUID).Scan(
			&recoveredContexts,
			&succeededContexts,
			&contextRows,
			&distinctFrontiers,
			&distinctConfigs,
			&firstAttempt,
			&lastAttempt,
			&producerOutputs,
			&locks,
			&wakeups,
		); err != nil {
			return false, err.Error()
		}
		complete := outputs == 1 &&
			recoveredContexts == 1 &&
			succeededContexts == 1 &&
			contextRows == 2 &&
			distinctFrontiers == 1 &&
			distinctConfigs == 1 &&
			firstAttempt == 1 &&
			lastAttempt == 2 &&
			producerOutputs == 1 &&
			locks == 0 &&
			wakeups == 0
		return complete,
			fmt.Sprintf(
				"recovery output/old/new/rows/frontiers/configs/attempts/producer/locks/wakeups = %d/%d/%d/%d/%d/%d/%d-%d/%d/%d/%d, want 1/1/1/2/1/1/1-2/1/0/0; worker=%s",
				outputs,
				recoveredContexts,
				succeededContexts,
				contextRows,
				distinctFrontiers,
				distinctConfigs,
				firstAttempt,
				lastAttempt,
				producerOutputs,
				locks,
				wakeups,
				secondWorker.logExcerpt(),
			)
	})
	if got := requestCount.Load(); got != 2 {
		t.Fatalf("provider requests after maintenance recovery = %d, want 2", got)
	}
}
