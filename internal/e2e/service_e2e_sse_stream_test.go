//go:build integration && servicee2e

package e2e

import (
	"bufio"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServiceE2ESSEStreamDeliversLiveWakeup(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "sse-stream-wakeup")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "sse-stream-wakeup", "openai-prod", "service-e2e-local")
	agentID := project.createAgent(t, ctx)

	streamReq, err := env.newAPIRequest(
		ctx,
		http.MethodGet,
		project.projectPath+"/agents/"+agentID+"/events/stream",
		nil,
	)
	if err != nil {
		t.Fatalf("build sse request: %v", err)
	}
	streamReq.Header.Set("Authorization", "Bearer "+project.adminToken)
	streamReq.Header.Set("Last-Event-ID", "1")
	resp, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		t.Fatalf("sse request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("sse status=%d", resp.StatusCode)
	}

	scanner := bufio.NewScanner(resp.Body)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), ":") {
		t.Fatalf("sse stream missing preamble: %q", scanner.Text())
	}
	frames := make(chan string, 16)
	go func() {
		for scanner.Scan() {
			line := scanner.Text()
			if line == "" {
				continue
			}
			select {
			case frames <- line:
			case <-ctx.Done():
				return
			}
		}
	}()

	select {
	case line := <-frames:
		if line != ": heartbeat" {
			t.Fatalf("first idle stream frame=%q, want heartbeat comment", line)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("sse stream did not deliver a heartbeat through the real API service")
	}

	postStart := time.Now()
	project.createInput(t, ctx, agentID, "service-e2e live wakeup payload")
	env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
		ProviderConfig: "openai-prod",
		BaseURL:        "http://127.0.0.1:1",
	})

	deadline := time.After(10 * time.Second)
	sawWakeupFrame := false
	for !sawWakeupFrame {
		select {
		case line := <-frames:
			if strings.HasPrefix(line, "data: ") && strings.Contains(line, "live wakeup payload") {
				sawWakeupFrame = true
			}
		case <-deadline:
			t.Fatalf(
				"sse did not receive live wakeup frame within 10s; " +
					"the Redis publish/subscribe path through the real cmd/api binary is not working",
			)
		}
	}
	elapsed := time.Since(postStart)
	t.Logf("service E2E SSE wakeup delivered in %s", elapsed)
	if elapsed > 5*time.Second {
		t.Logf("warning: live wakeup arrived in %s (publish path may be slower than expected)", elapsed)
	}
}
