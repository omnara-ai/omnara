//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type failedIntegrationBlobs struct {
	content map[string][]byte
	deleted []string
	failKey string
	failure error
}

func (b *failedIntegrationBlobs) PutBlob(_ context.Context, key string, content []byte) (blobstore.Metadata, error) {
	b.content[key] = append([]byte(nil), content...)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *failedIntegrationBlobs) GetBlob(_ context.Context, key string) ([]byte, blobstore.Metadata, error) {
	content, found := b.content[key]
	if !found {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return append([]byte(nil), content...), blobstore.Metadata{
		Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
	}, nil
}

func (b *failedIntegrationBlobs) DeleteBlob(_ context.Context, key string) error {
	b.deleted = append(b.deleted, key)
	if key == b.failKey {
		return b.failure
	}
	if _, found := b.content[key]; !found {
		return blobstore.ErrNotFound
	}
	delete(b.content, key)
	return nil
}

func failedIntegrationFile(id uuid.UUID, content []byte) IntegrationPlannedFile {
	return IntegrationPlannedFile{
		ArtifactID: id, ProviderFileID: "provider-file-" + id.String(),
		Expected: &artifactstore.PreparedArtifact{
			ID: id, ContentType: "text/plain", Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
		},
	}
}

type failedArtifactCleanupSpy struct {
	*artifactstore.Store
	attempted []uuid.UUID
}

func (s *failedArtifactCleanupSpy) DeleteUnreferencedPreparedArtifact(
	ctx context.Context, projectID, agentID, artifactID uuid.UUID,
) error {
	s.attempted = append(s.attempted, artifactID)
	return s.Store.DeleteUnreferencedPreparedArtifact(ctx, projectID, agentID, artifactID)
}

func TestFailedIntegrationInboxArtifactCleanup(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, integrationID := integrationWorkerFixture(t)
	inbox := store.Integrations()
	blobs := &failedIntegrationBlobs{content: make(map[string][]byte)}
	artifacts := artifactstore.New(pool, blobs)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	agentID := uuid.New()
	_, err := pool.Exec(
		ctx,
		`INSERT INTO agents(id, org_id, project_id, state, current_config_id, created_at, updated_at)
VALUES($1,$2,$3,'active',$4,now(),now())`,
		agentID,
		ids.OrgID,
		ids.ProjectID,
		base.ID,
	)
	require.NoError(t, err)
	durable, err := artifacts.CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: ids.ProjectID, AgentID: agentID, ContentType: "text/plain", Content: []byte("accepted history"),
	})
	require.NoError(t, err)
	plannedAgent := uuid.New()
	content := []byte("pinned upload")
	files := []IntegrationPlannedFile{
		failedIntegrationFile(
			uuid.New(),
			content,
		),
		failedIntegrationFile(uuid.New(), content),
		failedIntegrationFile(uuid.New(), content),
	}
	for _, file := range files[:2] {
		require.NoError(t, artifacts.UploadPreparedArtifact(ctx, plannedAgent, *file.Expected, content))
	}
	plan := IntegrationInboxPlan{
		"uncommitted": {
			AgentID: plannedAgent, ArtifactIDs: integrationArtifactIDs(files), Files: files,
			Input: &executionstore.CreateAgentContentInputInput{ProjectID: ids.ProjectID, AgentID: plannedAgent},
		},
		"referenced": {
			AgentID: agentID, ArtifactIDs: []uuid.UUID{durable.ID},
			Input: &executionstore.CreateAgentContentInputInput{ProjectID: ids.ProjectID, AgentID: agentID},
		},
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "failure-files", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	claimed, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, claimed.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if err := work.FreezePlan(ctx, raw); err != nil {
				return err
			}
			return work.Fail(ctx, "fixture failed before preparation")
		}))
	blobs.failure = errors.New("temporary object-store failure")
	blobs.failKey = "artifacts/" + plannedAgent.String() + "/" + files[0].ArtifactID.String()
	require.ErrorIs(
		t,
		CleanupTerminalIntegrationInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
		blobs.failure,
	)
	retained, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxFailed, retained.State)
	require.JSONEq(t, string(raw), string(retained.Plan))
	require.Len(t, blobs.content, 2, "only durable history and the failed deletion remain")
	blobs.failKey = ""
	for range 2 {
		require.NoError(t, CleanupTerminalIntegrationInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID))
	}
	require.Len(t, blobs.content, 1)
	require.NotContains(t, blobs.deleted, "artifacts/"+agentID.String()+"/"+durable.ID.String())
	stored, _, err := artifacts.GetArtifactBlob(ctx, ids.ProjectID, agentID, durable.ID)
	require.NoError(t, err)
	require.Equal(t, "accepted history", string(stored))
	require.ErrorIs(t, artifacts.DeleteUnreferencedPreparedArtifact(ctx, uuid.New(), agentID, durable.ID),
		storeerr.ErrUnauthorized)
	require.ErrorIs(t, artifactstore.New(pool, nil).DeleteUnreferencedPreparedArtifact(
		ctx, ids.ProjectID, plannedAgent, files[0].ArtifactID), artifactstore.ErrBlobStoreNotConfigured)
	before := len(blobs.deleted)
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	require.Error(
		t,
		artifacts.DeleteUnreferencedPreparedArtifact(canceled, ids.ProjectID, plannedAgent, files[0].ArtifactID),
	)
	require.Len(t, blobs.deleted, before, "failed database reads must never authorize deletion")
}

func TestFailedIntegrationInboxCleanupProtectsDurableArtifacts(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, integrationID := integrationWorkerFixture(t)
	inbox := store.Integrations()
	blobs := &failedIntegrationBlobs{content: make(map[string][]byte)}
	artifacts := artifactstore.New(pool, blobs)
	base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
		"instruction: help\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
	agentID := uuid.New()
	_, err := pool.Exec(
		ctx,
		`INSERT INTO agents(id, org_id, project_id, state, current_config_id, created_at, updated_at)
VALUES($1,$2,$3,'active',$4,now(),now())`,
		agentID,
		ids.OrgID,
		ids.ProjectID,
		base.ID,
	)
	require.NoError(t, err)
	durable, err := artifacts.CreateArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: ids.ProjectID, AgentID: agentID, ContentType: "text/plain", Content: []byte("committed input file"),
	})
	require.NoError(t, err)
	file := failedIntegrationFile(durable.ID, []byte("committed input file"))
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "failed launch", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	integration, err := inbox.UpdateProjectIntegration(ctx, integrationID, integrationstore.SaveProjectIntegrationInput{
		OrgID:           ids.OrgID,
		ProjectID:       ids.ProjectID,
		Name:            "chat",
		IntegrationType: integrationdefinition.SlackThread,
		Settings: integrationstore.ProjectIntegrationSettings{
			Launcher: &integrationstore.IntegrationLauncher{
				Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.IntegrationLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
			},
		},
	})
	require.NoError(t, err)
	unused := failedIntegrationFile(uuid.New(), []byte("unused launch bytes"))
	plannedAgent := uuid.New()
	require.NoError(
		t,
		artifacts.UploadPreparedArtifact(ctx, plannedAgent, *unused.Expected, []byte("unused launch bytes")),
	)
	plan := IntegrationInboxPlan{
		"partial": {
			AgentID: agentID, ArtifactIDs: []uuid.UUID{file.ArtifactID}, Files: []IntegrationPlannedFile{file},
			Input: &executionstore.CreateAgentContentInputInput{ProjectID: ids.ProjectID, AgentID: agentID},
		},
		"failed-launch": {
			AgentID: plannedAgent, ArtifactIDs: []uuid.UUID{unused.ArtifactID}, Files: []IntegrationPlannedFile{unused},
			Launch: &executionstore.InboxLaunchPlan{AgentConfigID: base.ID},
			Selection: &integrationstore.InboxIntegrationSelection{
				IntegrationID: integration.ID, Slot: "reviewer",
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			},
		},
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "partial-files", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	claimed, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, claimed.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if err := work.FreezePlan(ctx, raw); err != nil {
				return err
			}
			return work.Fail(ctx, "retained partial admission")
		}))
	_, err = pool.Exec(ctx, `INSERT INTO org_memberships(org_id,user_id,role,created_at) VALUES($1,$2,'owner',now())`,
		ids.OrgID, ids.ProviderAdminUserID)
	require.NoError(t, err)
	_, _, err = store.Execution().ArchiveAgent(ctx, ids.ProjectID, agentID,
		identitystore.NewUserPrincipal(ids.ProviderAdminUserID))
	require.NoError(t, err)
	cleaner := &failedArtifactCleanupSpy{Store: artifacts}
	require.NoError(t, CleanupTerminalIntegrationInboxArtifacts(ctx, inbox, cleaner, ids.ProjectID, receipt.ID))
	require.ElementsMatch(t, []uuid.UUID{unused.ArtifactID, durable.ID}, cleaner.attempted,
		"every planned artifact is checked against durable ownership")
	require.Equal(t, []string{"artifacts/" + plannedAgent.String() + "/" + unused.ArtifactID.String()}, blobs.deleted)
	require.Len(t, blobs.content, 1)
	stored, _, err := artifacts.GetArtifactBlob(ctx, ids.ProjectID, agentID, durable.ID)
	require.NoError(t, err)
	require.Equal(t, "committed input file", string(stored))
}

type archivingInboxArtifacts struct {
	*artifactstore.Store
	archivedAgent uuid.UUID
	archive       func() error
	skippedError  error
	siblingError  error
}

func (s *archivingInboxArtifacts) UploadPreparedArtifact(
	ctx context.Context, agentID uuid.UUID, prepared artifactstore.PreparedArtifact, content []byte,
) error {
	if err := s.Store.UploadPreparedArtifact(ctx, agentID, prepared, content); err != nil {
		return err
	}
	if agentID == s.archivedAgent {
		if err := s.archive(); err != nil {
			return err
		}
		return s.skippedError
	}
	return s.siblingError
}

func TestIntegrationInboxSkippedUploadsCleanupAndPreparationErrors(t *testing.T) {
	for _, scenario := range []string{
		"archive during upload", "uploaded before retry", "cleanup failure leaves orphan", "live sibling failure",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, integrationID := integrationWorkerFixture(t)
			_, err := pool.Exec(ctx, `INSERT INTO org_memberships(org_id,user_id,role,created_at)
				VALUES($1,$2,'owner',now())`, ids.OrgID, ids.ProviderAdminUserID)
			require.NoError(t, err)
			principal := identitystore.NewUserPrincipal(ids.ProviderAdminUserID)
			base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			agents := make([]uuid.UUID, 2)
			var launchSlots []integrationstore.IntegrationLaunchSlot
			for i, key := range []string{"archived", "active"} {
				launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: ids.ProjectID, AgentConfigID: base.ID, LaunchedBy: principal,
				})
				require.NoError(t, err)
				agents[i] = launched.Agent.ID
				launchSlots = append(launchSlots, integrationstore.IntegrationLaunchSlot{Key: key, AgentID: &agents[i]})
			}
			inbox := store.Integrations()
			_, err = inbox.UpdateProjectIntegration(ctx, integrationID, integrationstore.SaveProjectIntegrationInput{
				OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", IntegrationType: integrationdefinition.SlackThread,
				Settings: integrationstore.ProjectIntegrationSettings{Launcher: &integrationstore.IntegrationLauncher{
					Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123", Slots: launchSlots,
				}},
			})
			require.NoError(t, err)
			_, _, err = inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, ReceiptKey: "skip-upload", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, found)
			content := []byte("pinned upload")
			file := failedIntegrationFile(uuid.New(), content)
			event := IntegrationEvent{
				Event: integrationdefinition.Event{
					Scope: integrationdefinition.Scope{Slack: &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
					Kind:  "message", Mentioned: true,
				},
				SemanticKey: "message:skip-upload", Actor: integrationTestActor(t, integrationID, "U123"),
				ContentBlocks: json.RawMessage(`[{"type":"media_ref","artifact_id":"` + file.ArtifactID.String() + `"}]`),
				Files:         []IntegrationPlannedFile{file},
			}
			router := NewIntegrationRouter(store.Execution(), inbox)
			plan, err := freezeTestIntegrationEvents(ctx, router, receipt.Lease(), []IntegrationEvent{event})
			require.NoError(t, err)
			require.Len(t, plan, 2)
			blobs := &failedIntegrationBlobs{content: make(map[string][]byte)}
			artifacts := &archivingInboxArtifacts{
				Store: artifactstore.New(pool, blobs), archivedAgent: agents[0],
				archive: func() error {
					_, _, err := store.Execution().ArchiveAgent(ctx, ids.ProjectID, agents[0], principal)
					return err
				},
				skippedError: errors.New("archived recipient upload response lost"),
			}
			var skippedKey, liveKey string
			for key, slot := range plan {
				if slot.AgentID == agents[0] {
					skippedKey = key
				} else {
					liveKey = key
				}
				if scenario == "uploaded before retry" {
					prepared := *slot.Files[0].Expected
					require.NoError(t, artifacts.Store.UploadPreparedArtifact(ctx, slot.AgentID, prepared, content))
				}
			}
			skippedArtifact, liveArtifact := plan[skippedKey].ArtifactIDs[0], plan[liveKey].ArtifactIDs[0]
			skippedBlob := "artifacts/" + agents[0].String() + "/" + skippedArtifact.String()
			liveBlob := "artifacts/" + agents[1].String() + "/" + liveArtifact.String()
			if scenario == "uploaded before retry" {
				require.NoError(t, artifacts.archive())
			}
			if scenario == "cleanup failure leaves orphan" {
				blobs.failKey, blobs.failure = skippedBlob, errors.New("delete unavailable")
			}
			if scenario == "live sibling failure" {
				artifacts.siblingError = errors.New("live recipient upload response lost")
			}
			provider := &integrationConsumerProvider{file: IntegrationInboxFile{Content: content, ContentType: "text/plain"}}
			consumer := NewIntegrationInboxConsumer(router, inbox, artifacts,
				map[string]IntegrationInboxProvider{"slack": provider}, nil, testIntegrationLaunchWorkflow(router))
			results, err := consumer.Consume(ctx, receipt.Lease())
			if scenario == "live sibling failure" {
				require.ErrorIs(t, err, artifacts.siblingError)
				require.NotErrorIs(t, err, artifacts.skippedError, "only settled slots lose their preparation errors")
				require.Len(t, results, 1)
				require.Equal(t, executionstore.InboxInputSkipAgentArchived, results[0].Input.Skipped)
				require.Empty(t, blobs.deleted, "unfinished receipts cannot clean potentially admissible uploads")
				require.Len(t, blobs.content, 2)
				pending, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
				require.NoError(t, err)
				require.Equal(t, integrationstore.IntegrationInboxProcessing, pending.State)
				artifacts.siblingError = nil
				results, err = consumer.Consume(ctx, receipt.Lease())
				require.NoError(t, err)
			} else {
				require.NoError(t, err, "settled skips and best-effort cleanup must not fail a completed receipt")
			}
			require.Len(t, results, 2)
			var inputID uuid.UUID
			for _, result := range results {
				require.NotNil(t, result.Input)
				if result.Slot == skippedKey {
					require.Equal(t, executionstore.InboxInputSkipAgentArchived, result.Input.Skipped)
				} else {
					require.True(t, result.Input.Created)
					inputID = result.Input.AgentInput.ID
				}
			}
			latest, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
			require.NoError(t, err)
			require.Equal(t, integrationstore.IntegrationInboxCompleted, latest.State)
			if scenario == "cleanup failure leaves orphan" {
				_, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
					ProjectID: ids.ProjectID, IntegrationID: integrationID, LeaseDuration: time.Minute,
				})
				require.NoError(t, err)
				require.False(t, found, "completed receipts are not reclaimed for cleanup")
				require.Contains(t, blobs.content, skippedBlob, "best-effort cleanup can leave an orphan")
				blobs.failKey = ""
			}
			// Explicit replay tests idempotency; the worker does not retry completed receipts.
			replayed, err := consumer.Consume(ctx, receipt.Lease())
			require.NoError(t, err)
			require.Len(t, replayed, 2)
			for _, result := range replayed {
				require.False(t, result.Input.Created)
				if result.Slot == liveKey {
					require.Equal(t, inputID, result.Input.AgentInput.ID)
				}
			}
			require.NotContains(t, blobs.content, skippedBlob)
			require.Contains(t, blobs.deleted, skippedBlob)
			require.NotContains(t, blobs.deleted, liveBlob)
			require.Equal(t, content, blobs.content[liveBlob])
			_, err = artifacts.GetArtifact(ctx, ids.ProjectID, agents[0], skippedArtifact)
			require.ErrorIs(t, err, storeerr.ErrNotFound)
			_, _, err = store.Execution().ArchiveAgent(ctx, ids.ProjectID, agents[1], principal)
			require.NoError(t, err)
			require.NoError(t, artifacts.DeleteUnreferencedPreparedArtifact(ctx, ids.ProjectID, agents[1], liveArtifact))
			stored, _, err := artifacts.GetArtifactBlob(ctx, ids.ProjectID, agents[1], liveArtifact)
			require.NoError(t, err)
			require.Equal(t, content, stored, "durable input artifacts survive cleanup even after their agent is archived")
			require.NotContains(t, blobs.deleted, liveBlob)
		})
	}
}
