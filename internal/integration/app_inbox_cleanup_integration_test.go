//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type failedAppBlobs struct {
	content map[string][]byte
	deleted []string
	failKey string
	failure error
}

func (b *failedAppBlobs) PutBlob(_ context.Context, key string, content []byte) (blobstore.Metadata, error) {
	b.content[key] = append([]byte(nil), content...)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *failedAppBlobs) GetBlob(_ context.Context, key string) ([]byte, blobstore.Metadata, error) {
	content, found := b.content[key]
	if !found {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return append([]byte(nil), content...), blobstore.Metadata{
		Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
	}, nil
}

func (b *failedAppBlobs) DeleteBlob(_ context.Context, key string) error {
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

func failedAppFile(id uuid.UUID, content []byte) AppPlannedFile {
	return AppPlannedFile{
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

func TestFailedAppInboxArtifactCleanup(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, appID := appWorkerFixture(t)
	inbox := store.Integrations()
	blobs := &failedAppBlobs{content: make(map[string][]byte)}
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
	files := []AppPlannedFile{
		failedAppFile(
			uuid.New(),
			content,
		),
		failedAppFile(uuid.New(), content),
		failedAppFile(uuid.New(), content),
	}
	for _, file := range files[:2] {
		require.NoError(t, artifacts.UploadPreparedArtifact(ctx, plannedAgent, *file.Expected, content))
	}
	plan := AppInboxPlan{
		"uncommitted": {
			AgentID: plannedAgent, ArtifactIDs: appArtifactIDs(files), Files: files,
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
		ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: "failure-files", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	claimed, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
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
		CleanupTerminalAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
		blobs.failure,
	)
	retained, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxFailed, retained.State)
	require.JSONEq(t, `{}`, string(retained.Progress))
	require.JSONEq(t, string(raw), string(retained.Plan))
	require.Len(t, blobs.content, 2, "only durable history and the failed deletion remain")
	blobs.failKey = ""
	for range 2 {
		require.NoError(t, CleanupTerminalAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID))
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

func TestFailedAppInboxCleanupProtectsCommittedSlots(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, appID := appWorkerFixture(t)
	inbox := store.Integrations()
	blobs := &failedAppBlobs{content: make(map[string][]byte)}
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
	file := failedAppFile(durable.ID, []byte("committed input file"))
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "failed launch", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	app, err := inbox.UpdateProjectApp(ctx, appID, integrationstore.SaveProjectAppInput{
		OrgID:     ids.OrgID,
		ProjectID: ids.ProjectID,
		Name:      "chat",
		AppType:   appdefinition.SlackThread,
		Settings: integrationstore.ProjectAppSettings{
			Launcher: &integrationstore.AppLauncher{
				Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
			},
		},
	})
	require.NoError(t, err)
	unused := failedAppFile(uuid.New(), []byte("unused launch bytes"))
	plannedAgent := uuid.New()
	require.NoError(
		t,
		artifacts.UploadPreparedArtifact(ctx, plannedAgent, *unused.Expected, []byte("unused launch bytes")),
	)
	plan := AppInboxPlan{
		"partial": {
			AgentID: agentID, ArtifactIDs: []uuid.UUID{file.ArtifactID}, Files: []AppPlannedFile{file},
			Input: &executionstore.CreateAgentContentInputInput{ProjectID: ids.ProjectID, AgentID: agentID},
		},
		"failed-launch": {
			AgentID: plannedAgent, ArtifactIDs: []uuid.UUID{unused.ArtifactID}, Files: []AppPlannedFile{unused},
			Launch: &executionstore.InboxLaunchPlan{AgentConfigID: base.ID},
			Selection: &integrationstore.InboxAppSelection{
				AppID: app.ID, Slot: "reviewer",
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			},
		},
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: "partial-files", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	claimed, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, claimed.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			if err := work.FreezePlan(ctx, raw); err != nil {
				return err
			}
			if err := work.CommitSlot(ctx, "partial", json.RawMessage(`{"input":"already committed"}`)); err != nil {
				return err
			}
			return work.Fail(ctx, "retained partial admission")
		}))
	cleaner := &failedArtifactCleanupSpy{Store: artifacts}
	require.NoError(t, CleanupTerminalAppInboxArtifacts(ctx, inbox, cleaner, ids.ProjectID, receipt.ID))
	require.Equal(
		t,
		[]uuid.UUID{unused.ArtifactID},
		cleaner.attempted,
		"committed slot must never reach deletion helper",
	)
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

func TestAppInboxSkippedUploadsCleanupAndPreparationErrors(t *testing.T) {
	for _, scenario := range []string{
		"archive during upload", "already prepared", "cleanup failure leaves orphan", "live sibling failure",
	} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			pool, store, ids, appID := appWorkerFixture(t)
			_, err := pool.Exec(ctx, `INSERT INTO org_memberships(org_id,user_id,role,created_at)
				VALUES($1,$2,'owner',now())`, ids.OrgID, ids.ProviderAdminUserID)
			require.NoError(t, err)
			principal := identitystore.NewUserPrincipal(ids.ProviderAdminUserID)
			base := storagefixture.SeedAgentConfig(t, ctx, store.Models(), store.Execution(), ids.OrgID, ids.ProjectID,
				"instruction: review\nmodel:\n  provider_config: openai-prod\n  name: gpt-test\n")
			agents := make([]uuid.UUID, 2)
			var launchSlots []integrationstore.AppLaunchSlot
			for i, key := range []string{"archived", "active"} {
				launched, err := store.Execution().LaunchAgent(ctx, executionstore.LaunchAgentInput{
					ProjectID: ids.ProjectID, AgentConfigID: base.ID, LaunchedBy: principal,
				})
				require.NoError(t, err)
				agents[i] = launched.Agent.ID
				launchSlots = append(launchSlots, integrationstore.AppLaunchSlot{Key: key, AgentID: &agents[i]})
			}
			inbox := store.Integrations()
			_, err = inbox.UpdateProjectApp(ctx, appID, integrationstore.SaveProjectAppInput{
				OrgID: ids.OrgID, ProjectID: ids.ProjectID, Name: "chat", AppType: appdefinition.SlackThread,
				Settings: integrationstore.ProjectAppSettings{Launcher: &integrationstore.AppLauncher{
					Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123", Slots: launchSlots,
				}},
			})
			require.NoError(t, err)
			_, _, err = inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
				ProjectID: ids.ProjectID, AppID: appID, ReceiptKey: "skip-upload", Payload: []byte(`{}`),
			})
			require.NoError(t, err)
			receipt, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
				ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
			})
			require.NoError(t, err)
			require.True(t, found)
			content := []byte("pinned upload")
			file := failedAppFile(uuid.New(), content)
			event := AppEvent{
				Event: appdefinition.Event{
					Scope: appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.2"}},
					Kind:  "message", Mentioned: true,
				},
				SemanticKey: "message:skip-upload", Actor: appTestActor(t, appID, "U123"),
				ContentBlocks: json.RawMessage(`[{"type":"media_ref","artifact_id":"` + file.ArtifactID.String() + `"}]`),
				Files:         []AppPlannedFile{file},
			}
			router := NewAppRouter(store.Execution(), inbox)
			plan, err := freezeTestAppEvents(ctx, router, receipt.Lease(), []AppEvent{event})
			require.NoError(t, err)
			require.Len(t, plan, 2)
			blobs := &failedAppBlobs{content: make(map[string][]byte)}
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
				if scenario == "already prepared" {
					prepared := *slot.Files[0].Expected
					require.NoError(t, artifacts.Store.UploadPreparedArtifact(ctx, slot.AgentID, prepared, content))
					require.NoError(t, router.Prepare(ctx, receipt.Lease(), key, []artifactstore.PreparedArtifact{prepared}))
				}
			}
			skippedArtifact, liveArtifact := plan[skippedKey].ArtifactIDs[0], plan[liveKey].ArtifactIDs[0]
			skippedBlob := "artifacts/" + agents[0].String() + "/" + skippedArtifact.String()
			liveBlob := "artifacts/" + agents[1].String() + "/" + liveArtifact.String()
			if scenario == "already prepared" {
				require.NoError(t, artifacts.archive())
			}
			if scenario == "cleanup failure leaves orphan" {
				blobs.failKey, blobs.failure = skippedBlob, errors.New("delete unavailable")
			}
			if scenario == "live sibling failure" {
				artifacts.siblingError = errors.New("live recipient upload response lost")
			}
			provider := &appConsumerProvider{file: AppInboxFile{Content: content, ContentType: "text/plain"}}
			consumer := NewAppInboxConsumer(router, inbox, artifacts,
				map[string]AppInboxProvider{"slack": provider}, nil, testAppLaunchWorkflow(router))
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
					ProjectID: ids.ProjectID, AppID: appID, LeaseDuration: time.Minute,
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
