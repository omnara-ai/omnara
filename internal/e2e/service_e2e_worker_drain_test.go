//go:build integration && servicee2e

package e2e

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestServiceE2EWorkerDrainCompletesInFlightModelCall(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "worker-drain-completes-model-call")

	const drainedText = "the worker finished this call while draining"
	requestStarted := make(chan struct{})
	releaseResponse := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(releaseResponse) }) }
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		close(requestStarted)
		select {
		case <-releaseResponse:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(
			`{"id":"resp_worker_drain","status":"completed","output":[` +
				`{"id":"msg_worker_drain","type":"message","content":[` +
				`{"type":"output_text","text":"` + drainedText + `"}]}],` +
				`"usage":{"input_tokens":8,"output_tokens":6}}`,
		))
	}))
	defer openai.Close()
	defer release()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "worker-drain", "openai-prod", "service-e2e-local")
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "finish this turn while the worker drains")
	worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        openai.URL,
	})
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
	select {
	case <-requestStarted:
	case <-ctx.Done():
		t.Fatalf("provider request did not start: %v", ctx.Err())
	}

	if err := worker.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("signal worker: %v", err)
	}
	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		response, err := http.Get(env.workerURL + "/healthz")
		if err != nil {
			return true, ""
		}
		_ = response.Body.Close()
		return false, "worker health endpoint still serving after SIGTERM"
	})
	release()
	select {
	case <-worker.exited():
		if err := worker.lifecycle.exitErr; err != nil {
			t.Fatalf("worker exit after drain: %v\n%s", err, worker.logExcerpt())
		}
	case <-ctx.Done():
		t.Fatalf("worker did not exit after its in-flight model call finished: %v", ctx.Err())
	}

	var outputs, succeededContexts, contexts, locks int
	if err := env.db.QueryRow(ctx, `
SELECT (SELECT count(*)
        FROM agent_events event
        JOIN agents agent ON agent.id = event.agent_id
        JOIN content_blocks block
          ON block.agent_id = event.agent_id
         AND block.owner_model_output_id = event.model_output_id
        WHERE agent.project_id = $1
          AND event.agent_id = $2
          AND event.event_kind = 'model_output'
          AND block.block_kind = 'text'
          AND block.text_content = $3),
       (SELECT count(*) FILTER (WHERE attempt_number = 1 AND state = 'succeeded')
        FROM model_call_contexts
        WHERE project_id = $1 AND agent_id = $2),
       (SELECT count(*) FROM model_call_contexts WHERE project_id = $1 AND agent_id = $2),
       (SELECT count(*) FROM agent_runtime_locks WHERE agent_id = $2)
`, projectUUID, agentUUID, drainedText).Scan(&outputs, &succeededContexts, &contexts, &locks); err != nil {
		t.Fatalf("read drained model call: %v", err)
	}
	if outputs != 1 || succeededContexts != 1 || contexts != 1 || locks != 0 {
		t.Fatalf(
			"outputs/succeeded contexts/contexts/runtime locks = %d/%d/%d/%d, want 1/1/1/0",
			outputs, succeededContexts, contexts, locks,
		)
	}
}
