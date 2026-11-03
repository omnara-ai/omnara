//go:build integration

package tools

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/testutil/integrationblob"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordIntegrationFilesAndRevocation(t *testing.T) {
	for _, scenario := range []string{
		"artifact-and-memory", "read-only-memory", "empty-memory", "unattached-memory",
		"foreign-artifact", "deleted-memory", "disconnected-before-post",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			f := newIntegrationToolFixtureWithOptions(t, ctx, "discord-files", toolFixtureOptions{
				withDiscordIntegration: true, withToolContext: true, withMemory: true,
			}, storage.WithBlobStore(integrationblob.MustOpen(t, ctx)))
			scope := memorystore.Scope{
				OrgID: toolsTestOrgID, ProjectID: toolsTestProjectID, Principal: toolsTestUserPrincipal(f.User.ID),
			}
			memory, err := f.Store.Memories().Resolve(ctx, toolsTestProjectID, "engineering")
			require.NoError(t, err)
			if scenario == "unattached-memory" {
				memory, err = f.Store.Memories().Create(ctx, scope, "unattached", "", agentconfig.MemoryStoreAccessReadWrite)
				require.NoError(t, err)
			}
			memoryBytes := []byte{0, 255, 1, 2}
			if scenario == "empty-memory" {
				memoryBytes = nil
			}
			_, err = f.Store.Memories().Write(ctx, memorystore.WriteInput{
				Scope: scope, StoreID: memory.ID, Path: "reports/chart.bin", Content: memoryBytes,
			})
			require.NoError(t, err)
			if scenario == "read-only-memory" {
				access := agentconfig.MemoryStoreAccessRead
				_, err = f.Store.Memories().Update(ctx, scope, memory.ID, nil, &access)
				require.NoError(t, err)
			}
			artifactAgentID := f.Agent.ID
			if scenario == "foreign-artifact" {
				other, err := f.Store.Execution().CreateAgentFixture(ctx, executionstore.AgentFixtureInput{
					ProjectID: toolsTestProjectID, CurrentConfigID: f.AgentConfig.ID,
				})
				require.NoError(t, err)
				artifactAgentID = other.ID
			}
			artifact, err := f.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID: toolsTestProjectID, AgentID: artifactAgentID, Filename: "report.txt",
				ContentType: "text/plain", Content: []byte("artifact contents"), IdempotencyKey: "discord-attachment",
			})
			require.NoError(t, err)
			artifactID, err := publicid.Encode(publicid.KindArtifact, artifact.ID)
			require.NoError(t, err)
			input, err := json.Marshal(discordPostInput{
				Content: "report",
				Paths:   []string{"/artifacts/" + artifactID, "/memory/" + memory.Name + "/reports/chart.bin"},
			})
			require.NoError(t, err)
			call := f.recordToolCall(t, ctx, "post", "int__chat__post_message", string(input), f.Now)
			posts, scopeReads := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v10/users/@me":
					writeToolTestJSON(w, map[string]any{"id": "222", "bot": true})
				case "/v10/applications/@me":
					if scenario == "deleted-memory" {
						assert.NoError(t, f.Store.Memories().Delete(ctx, scope, memory.ID))
					}
					writeToolTestJSON(w, map[string]any{"id": "111"})
				case "/v10/channels/555":
					scopeReads++
					if scenario == "disconnected-before-post" {
						_, err := f.Store.Integrations().DisconnectIntegration(ctx, integrationstore.DisconnectIntegrationInput{
							ProjectID: toolsTestProjectID, IntegrationID: f.Install.ID,
							ExpectedSetupRevision: &f.Install.SetupRevision,
						})
						assert.NoError(t, err)
					}
					writeToolTestJSON(w, map[string]any{"id": "555", "parent_id": "444", "guild_id": "333", "type": 11})
				case "/v10/channels/555/messages":
					posts++
					assert.Equal(t, http.MethodPost, r.Method)
					if !assert.NoError(t, r.ParseMultipartForm(1024*1024)) {
						w.WriteHeader(http.StatusBadRequest)
						return
					}
					defer func() { assert.NoError(t, r.MultipartForm.RemoveAll()) }()
					var payload struct {
						Content      string `json:"content"`
						Nonce        string `json:"nonce"`
						EnforceNonce bool   `json:"enforce_nonce"`
					}
					assert.NoError(t, json.Unmarshal([]byte(r.FormValue("payload_json")), &payload))
					assert.Equal(t, "report", payload.Content)
					assert.True(t, payload.EnforceNonce)
					assert.NotEmpty(t, payload.Nonce)
					assert.Len(t, r.MultipartForm.File, 2)
					for index, expected := range []struct {
						filename string
						content  []byte
					}{{"report.txt", []byte("artifact contents")}, {"chart.bin", memoryBytes}} {
						file, header, err := r.FormFile(fmt.Sprintf("files[%d]", index))
						if !assert.NoError(t, err) {
							continue
						}
						content, err := io.ReadAll(file)
						assert.NoError(t, err)
						assert.NoError(t, file.Close())
						assert.Equal(t, expected.filename, header.Filename)
						assert.Equal(t, expected.content, content)
					}
					writeToolTestJSON(w, map[string]any{
						"id": "666", "channel_id": "555", "nonce": payload.Nonce,
						"author": map[string]any{"id": "222", "bot": true},
					})
				default:
					t.Errorf("unexpected provider request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			defer server.Close()
			executor := Executor{Store: f.Store, IntegrationHTTPClient: integrationProviderTestClient(server)}
			result, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
			require.NoError(t, err)
			record, err := f.Store.Execution().GetToolCall(ctx, toolsTestProjectID, f.Agent.ID, f.toolCallID(t, ctx, call.ID))
			require.NoError(t, err)
			body := toolResultMapFromTestParts(t, result.ContentParts)
			if scenario == "artifact-and-memory" || scenario == "read-only-memory" {
				require.Equal(t, executionstore.ToolResultOutcomeSucceeded, record.Outcome, string(result.ContentParts))
				require.Equal(t, "666", body["message_id"])
				require.Equal(t, 1, posts)
				replay, err := dispatchAsyncToolToTerminal(t, ctx, executor, f.turn(), call)
				require.NoError(t, err)
				require.JSONEq(t, string(result.ContentParts), string(replay.ContentParts))
				require.Equal(t, 1, posts)
			} else {
				require.Equal(t, executionstore.ToolResultOutcomeFailed, record.Outcome, string(result.ContentParts))
				require.Zero(t, posts)
				require.Equal(t, "integration_tool_failed", body["error_code"])
				if scenario == "empty-memory" {
					require.Contains(t, body["message"], "is empty")
				}
			}
			if scenario == "disconnected-before-post" {
				require.Equal(t, 1, scopeReads, "revocation must happen after file loading and before the POST")
			}
		})
	}
}
