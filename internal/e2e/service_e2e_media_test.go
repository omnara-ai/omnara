//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestServiceE2EMediaAttachmentRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "deterministic-media-round-trip")

	uploadedBytes := []byte("uploaded png bytes")
	uploadedBase64 := base64.StdEncoding.EncodeToString(uploadedBytes)
	const modelText = "the image shows uploaded png bytes"

	var sawExpandedImage atomic.Bool
	openai := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read OpenAI request: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		body := string(data)
		if strings.Contains(body, `"type":"input_image"`) && strings.Contains(body, "data:image/png;base64,"+uploadedBase64) {
			sawExpandedImage.Store(true)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(
			[]byte(
				`{"id":"resp_service_e2e_media","status":"completed","output":[` +
					`{"id":"msg_service_e2e_media","type":"message",` +
					`"content":[{"type":"output_text","text":"` + modelText +
					`"}]}],"usage":{"input_tokens":12,"output_tokens":8}}`,
			),
		)
	}))
	defer openai.Close()

	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "deterministic-media", "openai-prod", "service-e2e-local")
	agentID := project.createAgent(t, ctx)
	created := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agents/"+agentID+"/inputs", map[string]any{
		"content_blocks": []map[string]any{
			{"type": "text", "text": "what is in this image?"},
			{"type": "media", "media_type": "image/png", "filename": "upload.png", "data": uploadedBase64},
		},
	}, "idem-"+agentID+"-media-input", project.adminToken, http.StatusCreated)
	inputBlocks := created["agent_input"].(map[string]any)["content_blocks"].([]any)
	if len(inputBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %+v", inputBlocks)
	}
	uploadedArtifactID, _ := inputBlocks[1].(map[string]any)["artifact_id"].(string)
	if !strings.HasPrefix(uploadedArtifactID, "art_") {
		t.Fatalf("expected uploaded artifact id, got %+v", inputBlocks[1])
	}

	env.startWorker(
		t,
		ctx,
		project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: openai.URL},
	)
	projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
	agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)

	waitForServiceE2ECondition(t, ctx, func() (bool, string) {
		var text string
		err := env.db.QueryRow(ctx, `
			SELECT block.text_content
			FROM agent_events event
			JOIN agents agent ON agent.id = event.agent_id
			JOIN content_blocks block ON block.agent_id = event.agent_id AND block.owner_model_output_id = event.model_output_id
			WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind = 'text'
		`, projectUUID, agentUUID).Scan(&text)
		if err != nil {
			return false, "model output not recorded yet: " + err.Error()
		}
		return text == modelText, "model output text does not match"
	})
	if !sawExpandedImage.Load() {
		t.Fatal("provider request did not contain the inline input_image data URL")
	}

	for _, download := range []struct {
		name       string
		artifactID string
		want       []byte
	}{{name: "uploaded", artifactID: uploadedArtifactID, want: uploadedBytes}} {
		req, err := env.newAPIRequest(
			ctx,
			http.MethodGet,
			project.projectPath+"/agents/"+agentID+"/artifacts/"+download.artifactID+"/content",
			nil,
		)
		if err != nil {
			t.Fatalf("new download request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+project.adminToken)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("download %s artifact: %v", download.name, err)
		}
		content, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatalf("read %s artifact content: %v", download.name, err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s artifact download status=%d body=%s", download.name, resp.StatusCode, content)
		}
		if string(content) != string(download.want) {
			t.Fatalf("%s artifact content mismatch: got %d bytes", download.name, len(content))
		}
		if got := resp.Header.Get("Content-Type"); got != "image/png" {
			t.Fatalf("%s artifact content type = %q", download.name, got)
		}
	}
}
