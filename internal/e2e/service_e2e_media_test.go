//go:build integration && servicee2e

package e2e

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestServiceE2ELargeTextAttachmentsRemainReadableInTheSameTurn(t *testing.T) {
	for _, test := range []struct {
		name         string
		files        int
		bytesPerFile int
	}{
		{name: "large_json", files: 1, bytesPerFile: 4_000_000},
		{name: "many_html_files", files: 15, bytesPerFile: 190_000},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
			defer cancel()
			env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "text-attachment-"+test.name)
			env.startAPI(t, ctx)
			project := env.bootstrapProjectViaAPIWithToolsAndModelOptions(t, ctx, test.name, "openai-prod", "service-e2e-local",
				serviceE2EConfiguredModelOptionsByIdentity{
					{ProviderConfigName: "openai-prod", ConfiguredModelName: "service-e2e-local"}: {
						ContextWindowTokens: 1_048_576, MaxOutputTokens: 8192, DefaultMaxOutputTokens: 1024,
					},
				}, "read_file", "search_files")
			agentID := project.createAgent(t, ctx)
			const instruction = "Read the end of the last attachment and report the verification marker."
			const marker = "verification_marker_from_original_file"
			blocks := []map[string]any{{"type": "text", "text": instruction}}
			var lastFile string
			for i := range test.files {
				filename := fmt.Sprintf("page-%d.html", i)
				content := strings.Repeat("x", test.bytesPerFile) + fmt.Sprintf("page-%d", i)
				if i == test.files-1 {
					content += marker
				}
				lastFile = "<html>" + content + "</html>"
				if test.files == 1 {
					filename = "archive.json"
					lastFile = `{"padding":"` + content + `"}`
				}
				blocks = append(blocks, map[string]any{
					"type": "media", "media_type": "text/plain", "filename": filename,
					"data": base64.StdEncoding.EncodeToString([]byte(lastFile)),
				})
			}
			created := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agents/"+agentID+"/inputs",
				map[string]any{"content_blocks": blocks}, "large-files", project.adminToken, http.StatusCreated)
			storedInput := testutil.RequireType[map[string]any](t, created["agent_input"])
			storedBlocks := testutil.RequireType[[]any](t, storedInput["content_blocks"])
			lastBlock := testutil.RequireType[map[string]any](t, storedBlocks[test.files])
			artifactID := testutil.RequireType[string](t, lastBlock["artifact_id"])
			artifactPath := "/artifacts/" + artifactID
			arguments, err := json.Marshal(map[string]any{
				"path": artifactPath, "offset_char": len(lastFile) - 100, "limit_chars": 100,
			})
			require.NoError(t, err)
			var calls atomic.Int64
			failures := make(chan string, 4)
			provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, readErr := io.ReadAll(r.Body)
				call := calls.Add(1)
				if readErr != nil || len(body) > 1_200_000 || !strings.Contains(string(body), instruction) ||
					!strings.Contains(string(body), artifactPath) {
					select {
					case failures <- fmt.Sprintf("request %d: bytes=%d, err=%v; oversized or missing instruction/file path",
						call, len(body), readErr):
					default:
					}
					http.Error(w, `{"error":{"message":"maximum context length exceeded"}}`, http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				if call == 1 {
					if strings.Contains(string(body), marker) {
						failures <- "unread file contents were inserted into the initial request"
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"id": "resp_attachment_read", "status": "completed",
						"output": []map[string]any{{"type": "function_call", "call_id": "read_attachment", "name": "read_file", "arguments": string(arguments)}},
						"usage":  map[string]int{"input_tokens": len(body) / 4, "output_tokens": 40},
					})
					return
				}
				if !strings.Contains(string(body), marker) || !strings.Contains(string(body), "function_call_output") {
					failures <- "read_file did not return the end of the original stored file"
				}
				text := "attachment analysis complete"
				if call > 2 {
					text = "next turn complete"
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"id": fmt.Sprintf("resp_attachment_%d", call), "status": "completed",
					"output": []map[string]any{{"type": "message", "id": fmt.Sprintf("msg_%d", call),
						"content": []map[string]string{{"type": "output_text", "text": text}}}},
					"usage": map[string]int{"input_tokens": len(body) / 4, "output_tokens": 10},
				})
			}))
			defer provider.Close()
			worker := env.startWorker(t, ctx, project.projectID, serviceWorkerOptions{
				ProviderConfig: "openai-prod", BaseURL: provider.URL,
			})
			projectUUID := mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID)
			agentUUID := mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID)
			waitForServiceE2ETextOutput(t, ctx, env, projectUUID, agentUUID, "attachment analysis complete", failures, worker)
			waitForServiceE2EAgentIdle(t, ctx, env, projectUUID, agentUUID)
			require.Equal(t, int64(2), calls.Load())
			project.createInput(t, ctx, agentID, "Continue with the same files.")
			waitForServiceE2ETextOutput(t, ctx, env, projectUUID, agentUUID, "next turn complete", failures, worker)
			require.Equal(t, int64(3), calls.Load())
			var failedOrCompacted int
			require.NoError(t, env.db.QueryRow(ctx,
				`SELECT count(*) FROM model_call_contexts WHERE agent_id = $1 AND (state = 'failed' OR operation_kind = 'compaction')`, agentUUID,
			).Scan(&failedOrCompacted))
			require.Zero(t, failedOrCompacted)
			req, err := env.newAPIRequest(ctx, http.MethodGet,
				project.projectPath+"/agents/"+agentID+"/artifacts/"+artifactID+"/content", nil)
			require.NoError(t, err)
			req.Header.Set("Authorization", "Bearer "+project.adminToken)
			response, err := http.DefaultClient.Do(req)
			require.NoError(t, err)
			defer response.Body.Close()
			stored, err := io.ReadAll(response.Body)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, response.StatusCode)
			require.Equal(t, sha256.Sum256([]byte(lastFile)), sha256.Sum256(stored))
		})
	}
}

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
	inputBlocks := testutil.RequireType[[]any](
		t, testutil.RequireType[map[string]any](t, created["agent_input"])["content_blocks"],
	)
	if len(inputBlocks) != 2 {
		t.Fatalf("expected 2 content blocks, got %+v", inputBlocks)
	}
	uploadedArtifactID, _ := testutil.RequireType[map[string]any](t, inputBlocks[1])["artifact_id"].(string)
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
			WHERE agent.project_id = $1 AND event.agent_id = $2 AND event.event_kind = 'model_output' AND block.block_kind =
			    'text'
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
