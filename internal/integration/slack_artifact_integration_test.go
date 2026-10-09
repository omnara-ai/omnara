//go:build integration

package integration

import (
	"bytes"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSlackInboxAttachmentAdmissionWithoutListBucket(t *testing.T) {
	for _, stage := range []string{"fresh", "frozen before upload"} {
		t.Run(stage, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, integrationID := integrationWorkerFixture(t)
			inbox := store.Integrations()
			setup, err := inbox.GetIntegration(ctx, ids.ProjectID, integrationID)
			require.NoError(t, err)
			base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			agent, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
				ProjectID: ids.ProjectID, AgentConfigID: base.ID,
				LaunchedBy: identitystore.NewUserPrincipal(ids.ProviderAdminUserID),
			})
			require.NoError(t, err)
			createTestIntegrationSubscription(t, store, setup, agent.Agent.ID, `{"channel_id":"D123"}`)
			var pngBytes bytes.Buffer
			require.NoError(t, png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 2, 2))))
			content := pngBytes.Bytes()
			var downloads atomic.Int32
			slackServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth.test":
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
				case "/users.info":
					_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"display_name":"Alex"}}}`))
				case "/conversations.info":
					_, _ = w.Write([]byte(`{"ok":true,"channel":{"name":"direct"}}`))
				case "/image.png":
					downloads.Add(1)
					assert.Equal(t, "Bearer xoxb-inbox-test", r.Header.Get("Authorization"))
					w.Header().Set("Content-Type", "image/png")
					_, _ = w.Write(content)
				case "/reactions.add":
					_, _ = w.Write([]byte(`{"ok":true}`))
				default:
					t.Errorf("unexpected Slack request %s", r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
				}
			}))
			t.Cleanup(slackServer.Close)
			provider := NewSlackIntegrationInboxProvider(
				slack.OAuthConfig{APIURL: slackServer.URL, HTTPClient: slackServer.Client()},
				store.Secrets(), inbox, store.Execution(),
			)
			payload := slackInboxTestPayload(t, slack.Event{
				Type: "message", Subtype: "file_share", Channel: "D123", ChannelType: "im",
				TS: "1791569974.211609", User: "U123", Text: "Please review this image",
				Files: []slack.File{{ID: "F123", Name: "image.png", Mimetype: "image/png",
					Size: int64(len(content)), URLPrivateDownload: slackServer.URL + "/image.png"}},
			})
			_, _, err = inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "Ev123", Payload: payload,
			})
			require.NoError(t, err)
			receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, found)
			blobs, s3State := newInboxS3Store(t, inboxS3Options{})
			artifacts := artifactstore.New(pool, blobs)
			router := NewIntegrationRouter(store.Execution(), inbox)
			if stage != "fresh" {
				expansion, err := provider.Expand(ctx, setup, payload)
				require.NoError(t, err)
				_, err = freezeTestIntegrationEvent(ctx, router, receipt.Lease(), expansion.Event)
				require.NoError(t, err)
			}
			consumer := NewIntegrationInboxConsumer(router, inbox, artifacts,
				map[integrationdefinition.Provider]IntegrationInboxProvider{integrationdefinition.ProviderSlack: provider},
				nil, testIntegrationLaunchWorkflow(router))
			results, err := consumer.Consume(ctx, receipt.Lease())
			require.NoError(t, err)
			_, admissionRequests := s3State.snapshot()
			wantRequests := []string{http.MethodPut}
			if stage == "frozen before upload" {
				wantRequests = []string{http.MethodGet, http.MethodPut}
			}
			require.Equal(t, wantRequests, admissionRequests)
			require.Len(t, results, 1)
			require.NotNil(t, results[0].Input)
			require.True(t, results[0].Input.Created)
			input := results[0].Input.AgentInput
			var textBlocks int
			require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM content_blocks
				WHERE owner_agent_input_id=$1 AND block_kind='text' AND text_content=$2`,
				input.ID, "Please review this image").Scan(&textBlocks))
			require.Equal(t, 1, textBlocks)
			var admittedArtifact uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT artifact_id FROM content_blocks
				WHERE owner_agent_input_id=$1 AND block_kind='artifact'`, input.ID).Scan(&admittedArtifact))
			completed, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, completed.State)
			require.Equal(t, 1, completed.AttemptCount)
			plan, err := decodeIntegrationInboxPlan(completed.Plan)
			require.NoError(t, err)
			for _, recipient := range plan.Recipients {
				require.Len(t, recipient.ArtifactIDs, 1)
				require.Equal(t, recipient.ArtifactIDs[0], admittedArtifact)
				stored, record, err := artifacts.GetArtifactBlob(ctx, ids.ProjectID, recipient.AgentID, recipient.ArtifactIDs[0])
				require.NoError(t, err)
				require.Equal(t, content, stored)
				require.Equal(t, blobstore.ContentDigest(content), record.Digest)
				require.Equal(t, "image.png", record.Filename)
				require.Equal(t, "image/png", record.ContentType)
				require.Equal(t, int64(len(content)), *record.SizeBytes)
			}
			objects, _ := s3State.snapshot()
			require.Len(t, objects, 1)
			wantDownloads := int32(1)
			if stage == "frozen before upload" {
				wantDownloads++
			}
			require.Equal(t, wantDownloads, downloads.Load())
		})
	}
}
