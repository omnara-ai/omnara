//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type integrationConversationAuthorityCounter struct {
	IntegrationExecutionStore
	checks map[string]int
}

func (s *integrationConversationAuthorityCounter) CheckInboxConversationAuthority(
	ctx context.Context, lease integrationstore.IntegrationInboxLease, key string,
	address integrationstore.ConversationAddress,
) error {
	s.checks[key]++
	return s.IntegrationExecutionStore.CheckInboxConversationAuthority(ctx, lease, key, address)
}

func TestIntegrationDiscordFrozenArchivedRecipientsDoNotRequirePreparation(t *testing.T) {
	for _, scenario := range []string{"archived only with file", "active sibling with file", "revoked sibling"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, integrationID := integrationProviderFixture(t, "discord", "11", "22")
			_, err := pool.Exec(ctx, `INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now())`,
				ids.OrgID, ids.ProviderAdminUserID)
			require.NoError(t, err)
			integration, err := store.Integrations().GetProjectIntegration(ctx, ids.ProjectID, integrationID)
			require.NoError(t, err)
			config := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: Help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			principal := identitystore.NewUserPrincipal(ids.ProviderAdminUserID)
			var agents []uuid.UUID
			count := 2
			if scenario == "archived only with file" {
				count = 1
			}
			for range count {
				launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: ids.ProjectID, LaunchedBy: principal, AgentConfigID: config.ID,
				})
				require.NoError(t, err)
				agents = append(agents, launched.Agent.ID)
				createTestIntegrationSubscription(
					t,
					store,
					integration,
					launched.Agent.ID,

					`{"channel_id":"300"}`,
				)
			}
			f, provider := newDiscordInboxFixture(t)
			f.integrationSetup, provider.integrations = integration, store.Integrations()
			raw := discordInboxPayload(t, f.message)
			_, _, err = store.Integrations().AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "archive", Payload: raw,
			})
			require.NoError(t, err)
			receipt, found, err := store.Integrations().ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, found)
			event, eligible, err := NormalizeDiscordIntegrationEvent(integration, raw, f.channels["300"])
			require.NoError(t, err)
			require.True(t, eligible)
			if scenario != "revoked sibling" {
				id := uuid.New()
				event.Files = []IntegrationPlannedFile{
					{ArtifactID: id, ProviderFileID: "600", Expected: &artifactstore.PreparedArtifact{
						ID: id, Filename: "note.txt", ContentType: "text/plain",
						Digest: blobstore.ContentDigest([]byte("note")), SizeBytes: 4,
					}},
				}
				event.ContentBlocks, err = json.Marshal([]map[string]string{{"type": "media_ref", "artifact_id": id.String()}})
				require.NoError(t, err)
			}
			execution := &integrationConversationAuthorityCounter{
				IntegrationExecutionStore: store.Execution(),
				checks:                    map[string]int{},
			}
			router := NewIntegrationRouter(execution, store.Integrations())
			plan, err := freezeTestIntegrationEvents(ctx, router, receipt.Lease(), []IntegrationEvent{event})
			require.NoError(t, err)
			require.Len(t, plan, count)
			var activeKey, archivedKey string
			if scenario == "active sibling with file" {
				keys := make([]string, 0, len(plan))
				for key := range plan {
					keys = append(keys, key)
				}
				slices.Sort(keys)
				activeKey, archivedKey = keys[0], keys[1]
				agents[0], agents[1] = plan[archivedKey].AgentID, plan[activeKey].AgentID
			}
			_, _, err = store.Execution().ArchiveAgent(ctx, ids.ProjectID, agents[0], principal)
			require.NoError(t, err)
			var artifacts IntegrationArtifactUploader
			if scenario == "active sibling with file" {
				uploads := &discordInboxArtifacts{uploaded: map[uuid.UUID][]byte{}}
				for _, slot := range plan {
					if slot.AgentID != agents[0] {
						for _, file := range slot.Files {
							uploads.uploaded[file.ArtifactID] = []byte("note")
						}
					}
				}
				artifacts = uploads
			}
			consumer := NewIntegrationInboxConsumer(router, store.Integrations(), artifacts,
				map[string]IntegrationInboxProvider{"discord": provider}, nil, testIntegrationLaunchWorkflow(router))
			if scenario == "revoked sibling" {
				removeTestAgentSubscriptions(t, store, integration, agents[1])
				_, err := consumer.Consume(ctx, receipt.Lease())
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				require.Zero(t, f.posts)
				createTestIntegrationSubscription(t, store, integration, agents[1], `{"channel_id":"300"}`)
			}
			results, err := consumer.Consume(ctx, receipt.Lease())
			require.NoError(t, err)
			if scenario == "active sibling with file" {
				require.Zero(t, execution.checks[archivedKey], "archival settles the recipient before live authority checks")
				require.Greater(t, execution.checks[activeKey], 1, "provider requests must recheck live authority")
			}
			require.Len(t, results, count)
			for _, result := range results {
				require.NotNil(t, result.Input)
				if plan[result.Slot].AgentID == agents[0] {
					require.Equal(t, executionstore.InboxInputSkipAgentArchived, result.Input.Skipped)
				} else {
					require.True(t, result.Input.Created)
				}
			}
			if count == 1 {
				require.Empty(t, f.requests, "archived-only slots must need neither Discord nor artifact preparation")
			}
			latest, err := store.Integrations().GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, latest.State)
		})
	}
}
