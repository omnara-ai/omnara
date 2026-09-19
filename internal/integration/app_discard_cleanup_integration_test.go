//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/storagefixture"
	"github.com/stretchr/testify/require"
)

type discardedAppBlobs struct {
	content map[string][]byte
	deleted []string
	failKey string
	failure error
}

func (b *discardedAppBlobs) PutBlob(_ context.Context, key string, content []byte) (blobstore.Metadata, error) {
	b.content[key] = append([]byte(nil), content...)
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *discardedAppBlobs) GetBlob(_ context.Context, key string) ([]byte, blobstore.Metadata, error) {
	content, found := b.content[key]
	if !found {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return append([]byte(nil), content...), blobstore.Metadata{
		Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
	}, nil
}

func (b *discardedAppBlobs) DeleteBlob(_ context.Context, key string) error {
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

func discardedAppFile(id uuid.UUID, content []byte) AppPlannedFile {
	return AppPlannedFile{
		ArtifactID: id, ProviderFileID: "provider-file-" + id.String(),
		Expected: &artifactstore.PreparedArtifact{
			ID: id, ContentType: "text/plain", Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
		},
	}
}

type discardedArtifactCleanupSpy struct {
	*artifactstore.Store
	attempted []uuid.UUID
}

func (s *discardedArtifactCleanupSpy) DeleteUnreferencedPreparedArtifact(
	ctx context.Context, projectID, agentID, artifactID uuid.UUID,
) error {
	s.attempted = append(s.attempted, artifactID)
	return s.Store.DeleteUnreferencedPreparedArtifact(ctx, projectID, agentID, artifactID)
}

func TestDiscardedAppInboxArtifactCleanup(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, connectionID := appWorkerFixture(t)
	inbox := store.Integrations()
	blobs := &discardedAppBlobs{content: make(map[string][]byte)}
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
		discardedAppFile(
			uuid.New(),
			content,
		),
		discardedAppFile(uuid.New(), content),
		discardedAppFile(uuid.New(), content),
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
		ProjectID: ids.ProjectID, ConnectionID: connectionID, ReceiptKey: "discard-files", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	claimed, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, ConnectionID: connectionID, LeaseDuration: time.Minute,
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
	require.ErrorIs(t, CleanupDiscardedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
		storeerr.ErrStateTransitionConflict)
	require.Empty(t, blobs.deleted)
	// Explicit receipt retry must preserve these same bytes, too.
	require.NoError(t, inbox.RetryFailedIntegrationInbox(ctx, ids.ProjectID, receipt.ID))
	require.ErrorIs(t, CleanupDiscardedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
		storeerr.ErrStateTransitionConflict)
	claimed, found, err = inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, ConnectionID: connectionID, LeaseDuration: time.Minute,
	})
	require.NoError(t, err)
	require.True(t, found)
	require.NoError(t, inbox.WithIntegrationInboxLease(ctx, claimed.Lease(),
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.Fail(ctx, "still failed") }))
	require.NoError(t, inbox.DiscardFailedIntegrationInbox(ctx, ids.ProjectID, receipt.ID))
	// Blob deletion errors do not undo discard or stop cleanup of other files.
	blobs.failure = errors.New("temporary object-store failure")
	blobs.failKey = "artifacts/" + plannedAgent.String() + "/" + files[0].ArtifactID.String()
	require.ErrorIs(
		t,
		CleanupDiscardedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
		blobs.failure,
	)
	retained, err := inbox.GetIntegrationInbox(ctx, ids.ProjectID, receipt.ID)
	require.NoError(t, err)
	require.Equal(t, integrationstore.IntegrationInboxDiscarded, retained.State)
	require.JSONEq(t, `{}`, string(retained.Progress))
	require.JSONEq(t, string(raw), string(retained.Plan))
	require.Len(t, blobs.content, 2, "only durable history and the failed deletion remain")
	blobs.failKey = ""
	for range 2 {
		require.NoError(t, CleanupDiscardedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID))
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

func TestDiscardedAppInboxCleanupProtectsCommittedSlots(t *testing.T) {
	ctx := t.Context()
	pool, store, ids, connectionID := appWorkerFixture(t)
	inbox := store.Integrations()
	blobs := &discardedAppBlobs{content: make(map[string][]byte)}
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
	file := discardedAppFile(durable.ID, []byte("committed input file"))
	profile, err := store.Execution().CreateAgentProfile(ctx, executionstore.CreateAgentProfileInput{
		ProjectID: ids.ProjectID, Name: "failed launch", CurrentConfigID: base.ID,
	})
	require.NoError(t, err)
	connectionPublic, err := publicid.Encode(publicid.KindIntegrationConnection, connectionID)
	require.NoError(t, err)
	app, err := inbox.CreateProjectApp(ctx, integrationstore.SaveProjectAppInput{
		OrgID:        ids.OrgID,
		ProjectID:    ids.ProjectID,
		Name:         "discard launch",
		DefinitionID: appdefinition.Slack,
		Enabled:      true,
		Settings: integrationstore.ProjectAppSettings{
			Resource: agentconfig.AgentConfigAppResourceSource{
				Definition: appdefinition.Slack,
				Connection: connectionPublic,
			},
			Launcher: &integrationstore.AppLauncher{
				Trigger: "mention", ScopeKind: "workspace", ScopeRef: "T123",
				Slots: []integrationstore.AppLaunchSlot{{Key: "reviewer", AgentProfileID: &profile.ID}},
			},
		},
	})
	require.NoError(t, err)
	unused := discardedAppFile(uuid.New(), []byte("unused launch bytes"))
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
				AppID: app.ID, ConnectionID: connectionID, Slot: "reviewer",
				Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:123.456"},
			},
		},
	}
	raw, err := json.Marshal(plan)
	require.NoError(t, err)
	receipt, _, err := inbox.AcceptIntegrationReceipt(ctx, integrationstore.VerifiedIntegrationReceipt{
		ProjectID: ids.ProjectID, ConnectionID: connectionID, ReceiptKey: "partial-files", Payload: []byte(`{}`),
	})
	require.NoError(t, err)
	claimed, found, err := inbox.ClaimIntegrationInbox(ctx, integrationstore.ClaimIntegrationInboxInput{
		ProjectID: ids.ProjectID, ConnectionID: connectionID, LeaseDuration: time.Minute,
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
	require.ErrorIs(t, CleanupDiscardedAppInboxArtifacts(ctx, inbox, artifacts, ids.ProjectID, receipt.ID),
		storeerr.ErrStateTransitionConflict)
	require.Empty(t, blobs.deleted)
	require.Len(t, blobs.content, 2, "partial failed receipts retain every planned upload")
	// S1 allows this mixed receipt to be discarded: its committed input must
	// survive while the uncommitted launch, which created no target, is abandoned.
	require.NoError(t, inbox.DiscardFailedIntegrationInbox(ctx, ids.ProjectID, receipt.ID))
	cleaner := &discardedArtifactCleanupSpy{Store: artifacts}
	require.NoError(t, CleanupDiscardedAppInboxArtifacts(ctx, inbox, cleaner, ids.ProjectID, receipt.ID))
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
