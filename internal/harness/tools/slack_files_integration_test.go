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

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func TestSlackIntegrationRejectsInvalidFiles(t *testing.T) {
	ctx := context.Background()
	fixture := newIntegrationToolFixtureWithOptions(t, ctx, "send-unauthorized-files",
		toolFixtureOptions{withMemory: true, withSlackIntegration: true, withToolContext: true},
		storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
	)
	scope := memorystore.Scope{
		OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(fixture.User.ID),
	}
	unattached, err := fixture.Store.Memories().Create(
		ctx, scope, "unattached", "", agentconfig.MemoryStoreAccessReadWrite,
	)
	require.NoError(t, err)
	_, err = fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: scope, StoreID: unattached.ID, Path: "secret.txt", Content: []byte("private"),
	})
	require.NoError(t, err)
	otherProjectID := integrationToolTestID("send-files-other-project")
	_, err = fixture.Pool.Exec(ctx,
		`INSERT INTO projects(id, org_id, name, idempotency_key, created_at, updated_at)
VALUES ($1, $2, 'Other Project', 'send-files-other-project', statement_timestamp(), statement_timestamp())`,
		otherProjectID, toolsTestOrgID)
	require.NoError(t, err)
	otherScope := scope
	otherScope.ProjectID = otherProjectID
	foreign, err := fixture.Store.Memories().Create(
		ctx, otherScope, "foreign", "", agentconfig.MemoryStoreAccessReadWrite,
	)
	require.NoError(t, err)
	_, err = fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
		Scope: otherScope, StoreID: foreign.ID, Path: "secret.txt", Content: []byte("private"),
	})
	require.NoError(t, err)
	otherAgent, err := fixture.Store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
		ProjectID: toolsTestProjectID, CurrentConfigID: fixture.AgentConfig.ID,
	})
	require.NoError(t, err)
	artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: toolsTestProjectID, AgentID: otherAgent.ID, Filename: "secret.txt",
		ContentType: "text/plain", Content: []byte("private"), IdempotencyKey: "other-agent-file",
	})
	require.NoError(t, err)
	artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
	require.NoError(t, err)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		t.Errorf("invalid file triggered provider request: %s", r.URL.Path)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	executor := Executor{Store: fixture.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
	slackTarget := slack.MessageTarget{TargetRef: "chat", Channel: "C123", ThreadTS: "111.222", BotToken: "xoxb-test"}
	for _, filePath := range []string{
		"/memory/unattached/secret.txt", "/memory/foreign/secret.txt",
		"/memory/engineering/missing.txt", "/artifacts/" + artifactID,
	} {
		t.Run(filePath, func(t *testing.T) {
			_, err := executor.sendSlackFiles(ctx, fixture.turn(), slackTarget,
				slackPostInput{Text: "files", Paths: []string{filePath}})
			require.ErrorIs(t, err, storeerr.ErrNotFound)
		})
	}
	t.Run("empty memory file", func(t *testing.T) {
		store, err := fixture.Store.Memories().Resolve(ctx, toolsTestProjectID, "engineering")
		require.NoError(t, err)
		_, err = fixture.Store.Memories().Write(ctx, memorystore.WriteInput{
			Scope: scope, StoreID: store.ID, Path: "empty.txt", Content: nil,
		})
		require.NoError(t, err)
		_, err = executor.sendSlackFiles(ctx, fixture.turn(), slackTarget,
			slackPostInput{Text: "files", Paths: []string{"/memory/engineering/empty.txt"}})
		require.EqualError(t, err, "attachment /memory/engineering/empty.txt is empty")
	})
	require.Zero(t, requests)
}

func TestSlackIntegrationUploadsFilesWithSafeRetries(t *testing.T) {
	tests := []struct {
		name                   string
		memory                 bool
		readOnly               bool
		fileCount              int
		uploadURLFailures      int
		completionRateLimits   int
		completionStatus       int
		loseAfterPath          string
		wantCode               string
		wantUploadRequests     int
		wantCompletionRequests int
	}{
		{
			name: "mixed attachments from read-only store", memory: true, readOnly: true, fileCount: 2,
			wantCode: "delivered", wantUploadRequests: 2, wantCompletionRequests: 1,
		},
		{
			name: "memory changes during retry", memory: true, uploadURLFailures: 1,
			wantCode: "delivered", wantUploadRequests: 1, wantCompletionRequests: 1,
		},
		{name: "success", fileCount: 2, wantCode: "delivered", wantUploadRequests: 2, wantCompletionRequests: 1},
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
				toolFixtureOptions{withMemory: tt.memory, withSlackIntegration: true, withToolContext: true},
				storage.WithBlobStore(integrationblob.MustOpen(t, ctx)),
			)
			fileCount := tt.fileCount
			if fileCount == 0 {
				fileCount = 1
			}
			files := []struct {
				filename   string
				content    []byte
				fileID     string
				uploadPath string
			}{
				{filename: "report.txt", content: []byte("artifact contents"), fileID: "F123", uploadPath: "/upload/v1/artifact"},
				{filename: "chart.txt", content: []byte{0, 255, 1, 2}, fileID: "F456", uploadPath: "/upload/v1/chart"},
			}
			files = files[:fileCount]
			paths := make([]string, 0, fileCount)
			var memoryInput memorystore.WriteInput
			var memoryDigest string
			for fileIndex, file := range files {
				if tt.memory && fileIndex == len(files)-1 {
					resource, err := fixture.Store.Memories().Resolve(ctx, toolsTestProjectID, "engineering")
					require.NoError(t, err)
					memoryInput = memorystore.WriteInput{
						Scope: memorystore.Scope{
							OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID,
							Principal: toolsTestUserPrincipal(fixture.User.ID),
						},
						StoreID: resource.ID, Path: "reports/" + file.filename, Content: file.content,
					}
					written, err := fixture.Store.Memories().Write(ctx, memoryInput)
					require.NoError(t, err)
					memoryDigest = written.Digest
					if tt.readOnly {
						agentAccess := agentconfig.MemoryStoreAccessRead
						_, err = fixture.Store.Memories().Update(ctx, memoryInput.Scope, resource.ID, nil, &agentAccess)
						require.NoError(t, err)
					}
					paths = append(paths, "/memory/engineering/"+memoryInput.Path)
					continue
				}

				artifact, err := fixture.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
					ProjectID:      toolsTestProjectID,
					AgentID:        fixture.Agent.ID,
					ContentType:    "text/plain",
					Filename:       file.filename,
					Content:        file.content,
					MaxBytes:       1024,
					IdempotencyKey: seed + "-" + strconv.Itoa(fileIndex),
				})
				if err != nil {
					t.Fatalf("create artifact: %v", err)
				}
				artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
				if err != nil {
					t.Fatalf("encode artifact id: %v", err)
				}
				paths = append(paths, "/artifacts/"+artifactID)
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
						if tt.memory {
							update := memoryInput
							update.ExpectedDigest = &memoryDigest
							update.Content = []byte("changed after read")
							if _, err := fixture.Store.Memories().Write(ctx, update); err != nil {
								t.Errorf("update memory during retry: %v", err)
							}
						}
						w.WriteHeader(http.StatusInternalServerError)
						return
					}
					if err := r.ParseForm(); err != nil {
						t.Errorf("parse upload URL request: %v", err)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					fileIndex := requests[r.URL.Path] - tt.uploadURLFailures - 1
					if fileIndex >= len(files) {
						t.Errorf("unexpected upload URL request %d", requests[r.URL.Path])
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					file := files[fileIndex]
					if r.Form.Get("filename") != file.filename || r.Form.Get("length") != strconv.Itoa(len(file.content)) {
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
					fileIndex := 0
					if r.URL.Path == "/upload/v1/chart" {
						fileIndex = 1
					}
					if fileIndex >= len(files) {
						t.Errorf("unexpected artifact upload path %s", r.URL.Path)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					file := files[fileIndex]
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
					if len(payload.Files) != len(files) || payload.ChannelID != "C123" || payload.ThreadTS != "111.222" ||
						payload.InitialComment != "here is the report" {
						t.Errorf("completion payload = %+v", payload)
						http.Error(w, "test handler failed", http.StatusBadRequest)
						return
					}
					for fileIndex, file := range files {
						if payload.Files[fileIndex].ID != file.fileID || payload.Files[fileIndex].Title != file.filename {
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
				slackTarget := slack.MessageTarget{TargetRef: "chat", Channel: "C123", ThreadTS: "111.222", BotToken: "xoxb-test"}
				_, err := executor.sendSlackFiles(
					ctx,
					fixture.turn(),
					slackTarget,
					slackPostInput{Text: "here is the report", Paths: paths},
				)
				if !errors.Is(err, storeerr.ErrRuntimeLockInactive) {
					t.Fatalf("artifact send error = %v, want ErrRuntimeLockInactive", err)
				}
			} else {
				input, err := json.Marshal(map[string]any{
					"text":  "here is the report",
					"paths": paths,
				})
				require.NoError(t, err)
				call := fixture.recordToolCall(
					t,
					ctx,
					"call_"+seed,
					toolcatalog.IntegrationToolName("chat", toolcatalog.IntegrationOperationPostMessage),
					string(input),
					fixture.Now.Add(20*time.Second),
				)
				result, err := dispatchAsyncToolToTerminal(t, ctx, executor, slackIntegrationToolTurn(fixture), call)
				if err != nil {
					t.Fatalf("dispatch artifact send: %v", err)
				}
				body := toolResultMapFromTestParts(t, result.ContentParts)
				require.Equal(t, "chat", body["integration"])
				if tt.wantCode == "delivered" {
					require.Equal(t, "C123", body["channel_id"])
					require.Len(t, body["file_ids"], fileCount)
				} else {
					require.Equal(t, tt.wantCode, body["error_code"])
				}
			}
			if requests["/files.getUploadURLExternal"] != fileCount+tt.uploadURLFailures {
				t.Fatalf(
					"upload URL requests = %d, want %d", requests["/files.getUploadURLExternal"],
					fileCount+tt.uploadURLFailures,
				)
			}
			uploadRequests := requests["/upload/v1/artifact"] + requests["/upload/v1/chart"]
			if uploadRequests != tt.wantUploadRequests {
				t.Fatalf("upload requests = %d, want %d", uploadRequests, tt.wantUploadRequests)
			}
			if requests["/files.completeUploadExternal"] != tt.wantCompletionRequests {
				t.Fatalf("completion requests = %d, want %d", requests["/files.completeUploadExternal"], tt.wantCompletionRequests)
			}
		})
	}
}
