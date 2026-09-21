//go:build integration

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestSlackAppUploadsArtifactWithSafeRetries(t *testing.T) {
	tests := []struct {
		name                   string
		artifactCount          int
		uploadURLFailures      int
		completionRateLimits   int
		completionStatus       int
		loseAfterPath          string
		wantCode               string
		wantUploadRequests     int
		wantCompletionRequests int
	}{
		{name: "success", artifactCount: 2, wantCode: "delivered", wantUploadRequests: 2, wantCompletionRequests: 1},
		{
			name:                   "upload URL transient failure",
			uploadURLFailures:      1,
			wantCode:               "delivered",
			wantUploadRequests:     1,
			wantCompletionRequests: 1,
		},
		{
			name:                   "upload retries do not consume completion retry budget",
			uploadURLFailures:      2,
			completionRateLimits:   1,
			wantCode:               "delivered",
			wantUploadRequests:     1,
			wantCompletionRequests: 2,
		},
		{
			name:                   "completion rate limit",
			completionRateLimits:   1,
			wantCode:               "delivered",
			wantUploadRequests:     1,
			wantCompletionRequests: 2,
		},
		{
			name:                   "completion failure",
			completionStatus:       http.StatusInternalServerError,
			wantCode:               "delivery_unknown",
			wantUploadRequests:     1,
			wantCompletionRequests: 1,
		},
		{name: "ownership lost before content upload", loseAfterPath: "/files.getUploadURLExternal"},
		{name: "ownership lost before completion", loseAfterPath: "/upload/v1/artifact", wantUploadRequests: 1},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			seed := "send-artifact-" + strconv.Itoa(index)
			fixture := newIntegrationToolFixtureWithOptions(
				t,
				ctx,
				seed,
				toolFixtureOptions{withSlackApp: true, withToolContext: true},
				storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
			)
			artifactCount := tt.artifactCount
			if artifactCount == 0 {
				artifactCount = 1
			}
			artifactFiles := []struct {
				filename   string
				content    []byte
				fileID     string
				uploadPath string
			}{
				{
					filename:   "report.txt",
					content:    []byte("artifact contents"),
					fileID:     "F123",
					uploadPath: "/upload/v1/artifact",
				},
				{
					filename:   "chart.txt",
					content:    []byte("chart contents"),
					fileID:     "F456",
					uploadPath: "/upload/v1/chart",
				},
			}
			artifactFiles = artifactFiles[:artifactCount]
			artifactIDs := make([]string, 0, artifactCount)
			for artifactIndex, file := range artifactFiles {
				artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
					ProjectID:      toolsTestProjectID,
					AgentID:        fixture.Agent.ID,
					ContentType:    "text/plain",
					Filename:       file.filename,
					Content:        file.content,
					MaxBytes:       1024,
					IdempotencyKey: seed + "-" + strconv.Itoa(artifactIndex),
				})
				if err != nil {
					t.Fatalf("create artifact: %v", err)
				}
				artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
				if err != nil {
					t.Fatalf("encode artifact id: %v", err)
				}
				artifactIDs = append(artifactIDs, artifactID)
			}
			requests := make(map[string]int)
			loseOwnership := func() error {
				return fixture.Store.Execution().ReleaseAgentRuntimeLock(
					ctx,
					toolsTestProjectID,
					fixture.Agent.ID,
					fixture.Lock.ID,
				)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if serveSlackToolIdentity(w, r) {
					return
				}
				requests[r.URL.Path]++
				switch r.URL.Path {
				case "/files.getUploadURLExternal":
					if requests[r.URL.Path] <= tt.uploadURLFailures {
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse upload URL request: %v", err)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					artifactIndex := requests[r.URL.Path] - tt.uploadURLFailures - 1
					if artifactIndex >= len(artifactFiles) {
						t.Errorf("unexpected upload URL request %d", requests[r.URL.Path])
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					file := artifactFiles[artifactIndex]
					if r.Form.Get("filename") != file.filename ||
						r.Form.Get("length") != strconv.Itoa(len(file.content)) {
						t.Errorf("upload URL form = %v", r.Form)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					if tt.loseAfterPath == r.URL.Path {
						if err := loseOwnership(); err != nil {
							t.Errorf("release runtime lock: %v", err)
							http.Error(w, "test ownership change failed", http.StatusBadRequest)
							return
						}
					}
					writeToolTestJSON(w, map[string]any{
						"ok":         true,
						"upload_url": "https://files.slack.com" + file.uploadPath,
						"file_id":    file.fileID,
					})
				case "/upload/v1/artifact", "/upload/v1/chart":
					if r.Header.Get("Authorization") != "" {
						t.Errorf("file upload included authorization")
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					artifactIndex := 0
					if r.URL.Path == "/upload/v1/chart" {
						artifactIndex = 1
					}
					if artifactIndex >= len(artifactFiles) {
						t.Errorf("unexpected artifact upload path %s", r.URL.Path)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					file := artifactFiles[artifactIndex]
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Errorf("read uploaded artifact: %v", err)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					if string(body) != string(file.content) {
						t.Errorf("uploaded artifact = %q", body)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					if tt.loseAfterPath == r.URL.Path {
						if err := loseOwnership(); err != nil {
							t.Errorf("release runtime lock: %v", err)
							http.Error(w, "test ownership change failed", http.StatusBadRequest)
							return
						}
					}
					w.WriteHeader(http.StatusOK)
				case "/files.completeUploadExternal":
					var payload struct {
						Files []struct {
							ID    string `json:"id"`
							Title string `json:"title"`
						} `json:"files"`
						ChannelID      string `json:"channel_id"`
						ThreadTS       string `json:"thread_ts"`
						InitialComment string `json:"initial_comment"`
					}
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
						t.Errorf("decode completion payload: %v", err)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					if len(payload.Files) != len(artifactFiles) || payload.ChannelID != "C123" ||
						payload.ThreadTS != "111.222" ||
						payload.InitialComment != "here is the report" {
						t.Errorf("completion payload = %+v", payload)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					for artifactIndex, file := range artifactFiles {
						if payload.Files[artifactIndex].ID != file.fileID ||
							payload.Files[artifactIndex].Title != file.filename {
							t.Errorf("completion payload = %+v", payload)
							http.Error(w, "test handler failed", http.StatusBadRequest)
							return
						}
					}
					if requests[r.URL.Path] <= tt.completionRateLimits {
						w.Header().Set("Retry-After", "0")
						w.WriteHeader(http.StatusTooManyRequests)
						return
					}
					if tt.completionStatus != 0 {
						w.WriteHeader(tt.completionStatus)
						return
					}
					writeToolTestJSON(w, map[string]any{"ok": true})
				default:
					t.Errorf("unexpected integration provider path %s", r.URL.Path)
					http.Error(w, "test handler failed", http.StatusBadRequest)
					return
				}
			}))
			defer server.Close()

			executor := Executor{
				Store:                 fixture.Store,
				IntegrationHTTPClient: integrationProviderTestClient(server),
			}
			if tt.loseAfterPath != "" {
				slackTarget := slack.MessageTarget{
					TargetRef: "chat",
					Channel:   "C123",
					ThreadTS:  "111.222",
					BotToken:  "xoxb-test",
				}
				_, err := executor.sendSlackArtifacts(
					ctx,
					fixture.turn(),
					slackTarget,
					slackPostInput{Text: "here is the report", ArtifactIDs: artifactIDs},
				)
				if !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
					t.Fatalf("artifact send error = %v, want ErrRuntimeLockInactive", err)
				}
			} else {
				input, err := json.Marshal(map[string]any{
					"text":         "here is the report",
					"artifact_ids": artifactIDs,
				})
				require.NoError(t, err)
				call := fixture.recordToolCall(
					t,
					ctx,
					"call_"+seed,
					toolcatalog.AppToolName("chat", toolcatalog.AppOperationPostMessage),
					string(input),
					fixture.Now.Add(20*time.Second),
				)
				result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackAppToolTurn(fixture), call)
				if err != nil {
					t.Fatalf("dispatch artifact send: %v", err)
				}
				body := toolResultMapFromTestParts(t, result.ContentParts)
				require.Equal(t, "chat", body["app"])
				if tt.wantCode == "delivered" {
					require.Equal(t, "C123", body["channel_id"])
					require.Len(t, body["file_ids"], artifactCount)
				} else {
					require.Equal(t, tt.wantCode, body["code"])
				}
			}
			if requests["/files.getUploadURLExternal"] != artifactCount+tt.uploadURLFailures {
				t.Fatalf(
					"upload URL requests = %d, want %d", requests["/files.getUploadURLExternal"],
					artifactCount+tt.uploadURLFailures,
				)
			}
			uploadRequests := requests["/upload/v1/artifact"] + requests["/upload/v1/chart"]
			if uploadRequests != tt.wantUploadRequests {
				t.Fatalf("upload requests = %d, want %d", uploadRequests, tt.wantUploadRequests)
			}
			if requests["/files.completeUploadExternal"] != tt.wantCompletionRequests {
				t.Fatalf(
					"completion requests = %d, want %d",
					requests["/files.completeUploadExternal"],
					tt.wantCompletionRequests,
				)
			}
		})
	}
}
