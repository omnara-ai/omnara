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
		return blobstore.ErrNotFound // Also exercise backends that report missing objects.
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
	// The failed initial launch has no agent row. Its object IDs/digests exist
	// only in the plan: simulate a crash after upload, before PrepareSlot.
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
	// Blob deletion errors do not undo terminal failure or stop cleanup of other files.
	blobs.failure = errors.New("temporary object-store failure")
	blobs.failKey = "artifacts/" + plannedAgent.String() + "/" + files[0].ArtifactID.String()
	require.ErrorIs(
		t,
		CleanupFailedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
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
		require.NoError(t, CleanupFailedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID))
	}
	require.Len(t, blobs.content, 1)
	require.NotContains(t, blobs.deleted, "artifacts/"+agentID.String()+"/"+durable.ID.String())
	stored, _, err := artifacts.GetArtifactBlob(ctx, ids.ProjectID, agentID, durable.ID)
	require.NoError(t, err)
	require.Equal(t, "accepted history", string(stored))
	// A wrong project cannot turn an existing reference into an apparent miss.
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
			Launch: &executionstore.LaunchAgentInput{ProjectID: ids.ProjectID, AgentConfigID: base.ID},
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
	require.NoError(t, CleanupFailedAppInboxArtifacts(ctx, inbox, cleaner, ids.ProjectID, receipt.ID))
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
