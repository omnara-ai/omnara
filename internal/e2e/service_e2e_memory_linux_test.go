//go:build integration && servicee2e

package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/testutil"
)

func TestServiceE2EMemoryFileRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	env := newDaemonOnlyServiceE2EEnvironment(t, ctx, "memory-file-round-trip")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "memory", "openai-prod", "service-e2e-local")
	store := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/memory-stores",
		map[string]any{"name": "notes"}, "", project.adminToken, http.StatusCreated)
	storeID := testutil.RequireType[string](t, store["id"])
	const (
		path        = "/memory/notes/nested/status.txt"
		original    = "status: pending\n"
		updated     = "status: complete\n"
		finalOutput = "memory edit completed"
	)
	fileURL := project.projectPath + "/memory-stores/" + storeID + "/file?path=nested/status.txt"
	upload, err := env.newAPIRequest(ctx, http.MethodPut, fileURL, strings.NewReader(original))
	if err != nil {
		t.Fatal(err)
	}
	upload.Header.Set("Authorization", "Bearer "+project.adminToken)
	upload.Header.Set("Content-Type", "application/octet-stream")
	written := doServiceJSONRequest(t, upload, http.StatusOK)
	digest := testutil.RequireType[string](t, written["digest"])
	config := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-configs",
		map[string]any{"source_format": "yaml", "source": `instruction: Read, search, and edit the attached memory.
model:
  provider_config: openai-prod
  name: service-e2e-local
memory_stores:
  - name: notes
    access: read_write
`}, "", project.adminToken, http.StatusCreated)
	profile := env.requestJSON(t, ctx, http.MethodPost, project.projectPath+"/agent-profiles/"+project.agentID+"/config",
		map[string]any{
			"config":                     testutil.RequireType[string](t, config["id"]),
			"expected_current_config_id": project.configID,
		}, "memory-config", project.adminToken, http.StatusOK)
	project.configID = testutil.RequireType[string](
		t, testutil.RequireType[map[string]any](t, profile["current_config"])["id"],
	)

	var requests atomic.Int64
	modelFailures := make(chan string, 1)
	fail := func(w http.ResponseWriter, status int, format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		select {
		case modelFailures <- message:
		default:
		}
		http.Error(w, message, status)
	}
	decodeResult := func(output string, result any) error {
		decoder := json.NewDecoder(strings.NewReader(output))
		var status struct{ Outcome string }
		if err := decoder.Decode(&status); err != nil {
			return err
		}
		if status.Outcome != "succeeded" {
			return fmt.Errorf("tool failed: %s", output)
		}
		return decoder.Decode(result)
	}
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/responses" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			fail(w, http.StatusBadRequest, "decode model request: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		request := requests.Add(1)
		var read struct{ Path, Content, Digest string }
		if request > 1 {
			output := toolResultOutputForCall(body, "read")
			if err := decodeResult(output, &read); err != nil ||
				read.Path != path || read.Content != original || read.Digest != digest {
				fail(w, http.StatusBadRequest, "unexpected read result: %s (%v)", output, err)
				return
			}
		}
		switch request {
		case 1:
			if !fakeModelRequestContainsTools(w, body, fail, "read_file", "search_files", "write_file") {
				return
			}
			writeOpenAIFunctionCall(w, fail, "resp_read", "read", "read_file", map[string]any{"path": path})
		case 2:
			writeOpenAIFunctionCall(w, fail, "resp_search", "search", "search_files", map[string]any{
				"path": "/memory/notes/**/*.txt", "args": []string{"-F", "-e", "pending"},
			})
		case 3:
			var search struct {
				MatchCount int `json:"match_count"`
				Truncated  bool
				Lines      []struct{ Path, Text string }
			}
			output := toolResultOutputForCall(body, "search")
			if err := decodeResult(output, &search); err != nil ||
				search.Truncated || search.MatchCount != 1 || len(search.Lines) != 1 ||
				search.Lines[0].Path != path || search.Lines[0].Text != strings.TrimSpace(original) {
				fail(w, http.StatusBadRequest, "unexpected search result: %s (%v)", output, err)
				return
			}
			writeOpenAIFunctionCall(w, fail, "resp_edit", "edit", "write_file", map[string]any{
				"path": path, "script": "s/pending/complete/", "expected_digest": read.Digest,
			})
		case 4:
			var edit struct{ Path, Digest string }
			output := toolResultOutputForCall(body, "edit")
			if err := decodeResult(output, &edit); err != nil ||
				edit.Path != path || edit.Digest != blobstore.ContentDigest([]byte(updated)) {
				fail(w, http.StatusBadRequest, "unexpected edit result: %s (%v)", output, err)
				return
			}
			writeOpenAIMessage(w, fail, "resp_done", finalOutput)
		default:
			fail(w, http.StatusBadRequest, "unexpected model request %d", request)
		}
	}))
	defer model.Close()
	agentID := project.createAgent(t, ctx)
	project.createInput(t, ctx, agentID, "Read the note, find pending status, and mark it complete.")
	worker := env.startWorker(t, ctx, project.projectID,
		serviceWorkerOptions{ProviderConfig: "openai-prod", BaseURL: model.URL})
	waitForServiceE2ETextOutput(t, ctx, env,
		mustDecodeServiceE2EPublicID(t, publicid.KindProject, project.projectID),
		mustDecodeServiceE2EPublicID(t, publicid.KindAgent, agentID), finalOutput, modelFailures, worker)
	if requests.Load() != 4 {
		t.Fatalf("model requests = %d, want 4", requests.Load())
	}
	download, err := env.newAPIRequest(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	download.Header.Set("Authorization", "Bearer "+project.adminToken)
	response, err := http.DefaultClient.Do(download)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(content) != updated {
		t.Fatalf("download: status=%d content=%q error=%v", response.StatusCode, content, err)
	}
	if got := response.Header.Get("X-Omnara-File-Digest"); got != blobstore.ContentDigest(content) || got == digest {
		t.Fatalf("updated digest = %q", got)
	}
}
