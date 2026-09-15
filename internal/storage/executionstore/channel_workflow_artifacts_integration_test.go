//go:build integration

package executionstore_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

type workflowArtifactBlobs struct {
	mu        sync.Mutex
	content   map[string][]byte
	uploads   []string
	deletions []string
	afterPut  func(context.Context, string)
}

func (b *workflowArtifactBlobs) OpenBlob(context.Context, string) (io.ReadCloser, blobstore.Metadata, error) {
	return nil, blobstore.Metadata{}, errors.New("unexpected streaming read in workflow upload fixture")
}

func (b *workflowArtifactBlobs) PutBlob(ctx context.Context, key string, content []byte) (blobstore.Metadata, error) {
	b.mu.Lock()
	b.uploads = append(b.uploads, key)
	b.content[key] = append([]byte(nil), content...)
	b.mu.Unlock()
	if b.afterPut != nil {
		b.afterPut(ctx, key)
	}
	return blobstore.Metadata{Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content))}, nil
}

func (b *workflowArtifactBlobs) GetBlob(_ context.Context, key string) ([]byte, blobstore.Metadata, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	content, found := b.content[key]
	if !found {
		return nil, blobstore.Metadata{}, blobstore.ErrNotFound
	}
	return append([]byte(nil), content...), blobstore.Metadata{
		Digest: blobstore.ContentDigest(content), SizeBytes: int64(len(content)),
	}, nil
}

func (b *workflowArtifactBlobs) DeleteBlob(_ context.Context, key string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletions = append(b.deletions, key)
	delete(b.content, key)
	return nil
}

func (b *workflowArtifactBlobs) snapshot() (uploads, deletions []string, retained int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.uploads...), append([]string(nil), b.deletions...), len(b.content)
}

func newChannelWorkflowArtifactFixture(
	t *testing.T,
	ctx context.Context,
	name string,
) (channelWorkflowFixture, *workflowArtifactBlobs) {
	t.Helper()
	f := newChannelWorkflowFixture(t, ctx, name)
	blobs := &workflowArtifactBlobs{content: make(map[string][]byte)}
	// Reuse the canonical workflow's real installer, profile, route, definition,
	// and receipt fixtures; supply the same blob store to both capability stores.
	f.Store = newSecretIntegrationStore(f.Store.pool, storage.WithBlobStore(blobs))
	return f, blobs
}

func prepareWorkflowAttachment(
	t *testing.T,
	ctx context.Context,
	f channelWorkflowFixture,
	workflow executionstore.PreparedChannelWorkflow,
	key string,
	ordinal int,
	content []byte,
) executionstore.PreparedInputAttachment {
	t.Helper()
	prepared, err := f.Store.Artifacts().PrepareArtifact(ctx, artifactstore.CreateArtifactInput{
		ProjectID: f.Identity.ProjectID, AgentID: workflow.AgentID(), ContentType: "image/png", Filename: "photo.png",
		Content: content, MaxBytes: 1024, IdempotencyKey: key,
	})
	require.NoError(t, err)
	return executionstore.PreparedInputAttachment{
		Ordinal: ordinal, Artifact: prepared, Metadata: json.RawMessage(`{"provider_filename":"photo.png"}`),
	}
}

func workflowPhotoContent(attachment executionstore.PreparedInputAttachment) executionstore.PreparedInputContent {
	return executionstore.PreparedInputContent{
		Blocks:      json.RawMessage(`[{"type":"text","text":"hello"},{"type":"media_ref"}]`),
		Attachments: []executionstore.PreparedInputAttachment{attachment},
	}
}

func requireWorkflowArtifactInput(
	t *testing.T,
	ctx context.Context,
	f channelWorkflowFixture,
	result executionstore.ChannelInputResult,
	wantContent []byte,
) uuid.UUID {
	t.Helper()
	blocks, err := f.Store.q.ListContentBlocksForAgentInputs(ctx, dbsqlc.ListContentBlocksForAgentInputsParams{
		ProjectID: f.Identity.ProjectID, AgentID: result.AgentInput.AgentID, AgentInputIds: []uuid.UUID{result.AgentInput.ID},
	})
	require.NoError(t, err)
	require.Len(t, blocks, 2)
	require.Equal(t, "text", blocks[0].BlockKind)
	require.Equal(t, "hello", blocks[0].TextContent)
	require.Equal(t, "artifact", blocks[1].BlockKind)
	require.Equal(t, int32(1), blocks[1].Ordinal)
	require.NotNil(t, blocks[1].ArtifactID)
	require.JSONEq(t, `{"provider_filename":"photo.png"}`, string(blocks[1].Metadata))
	artifactID := *blocks[1].ArtifactID
	content, record, err := f.Store.Artifacts().GetArtifactBlob(
		ctx, f.Identity.ProjectID, result.AgentInput.AgentID, artifactID,
	)
	require.NoError(t, err)
	require.Equal(t, wantContent, content)
	require.Equal(t, blobstore.ContentDigest(wantContent), record.Digest)
	require.Equal(t, int64(len(wantContent)), *record.SizeBytes)
	require.Equal(t, "image/png", record.ContentType)
	require.Equal(t, "photo.png", record.Filename)
	require.JSONEq(t, fmt.Sprintf(`[
		{"type":"text","text":"hello"},
		{"type":"media_ref","artifact_id":%q,"metadata":{"provider_filename":"photo.png"}}
	]`, artifactID.String()), string(result.ContentBlocks))
	require.Equal(t, result.ChannelID, result.AgentInput.IntegrationTargetID)
	require.Equal(t, result.BindingID, result.AgentInput.IntegrationTargetBindingID)
	var wakeups int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_wakeups WHERE agent_id = $1`, result.AgentInput.AgentID,
	).Scan(&wakeups))
	require.Equal(t, 1, wakeups, "the committed media input must wake its agent")
	return artifactID
}

func requireWorkflowArtifactRowsAbsent(t *testing.T, ctx context.Context, f channelWorkflowFixture, agentID uuid.UUID) {
	t.Helper()
	for _, table := range []string{
		"integration_workflows", "artifacts", "agent_inputs", "content_blocks", "agent_wakeups",
	} {
		var count int
		require.NoError(t, f.Store.pool.QueryRow(ctx,
			fmt.Sprintf("SELECT count(*) FROM %s WHERE agent_id = $1", table), agentID,
		).Scan(&count))
		require.Zero(t, count, table)
	}
	_, err := f.Store.Execution().GetAgentInProject(ctx, f.Identity.ProjectID, agentID)
	require.ErrorIs(t, err, storeerr.ErrNotFound)
}

func TestChannelWorkflowArtifactsPrepareBeforeLaunchAndCommitCanonicalInput(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	f, blobs := newChannelWorkflowArtifactFixture(t, ctx, "workflow-media-atomic")
	input := f.event(t, ctx, "first")
	photo := []byte("first provider photo")
	blobs.afterPut = func(ctx context.Context, key string) {
		require.Contains(t, key, "/"+input.Prepared.AgentID().String()+"/")
		requireWorkflowArtifactRowsAbsent(t, ctx, f, input.Prepared.AgentID())
	}
	input.Content = workflowPhotoContent(prepareWorkflowAttachment(t, ctx, f, input.Prepared, "first:photo:0", 1, photo))
	blobs.afterPut = nil
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	_, err := blocker.Exec(ctx, `LOCK TABLE content_blocks IN SHARE MODE`)
	require.NoError(t, err)
	done := integrationdb.RunAsync(func() (executionstore.ChannelInputResult, error) {
		return f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	})
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "InsertContentBlock", 1)
	select {
	case result := <-done:
		t.Fatalf("workflow acknowledged input before its content could commit: %+v", result)
	default:
	}
	// This observes a separate DB connection after artifact persistence but
	// before content insertion: no launch/input/artifact state is yet visible.
	requireWorkflowArtifactRowsAbsent(t, ctx, f, input.Prepared.AgentID())
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 1)
	require.Empty(t, deletions)
	require.Equal(t, 1, retained)
	require.NoError(t, blocker.Commit(ctx))
	var result executionstore.ChannelInputResult
	select {
	case delivered := <-done:
		require.NoError(t, delivered.Err)
		result = delivered.Value
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	require.True(t, result.CreatedAgent)
	require.True(t, result.CreatedInput)
	require.Equal(t, input.Prepared.AgentID(), result.AgentInput.AgentID)
	canonicalID := requireWorkflowArtifactInput(t, ctx, f, result, photo)
	require.Equal(t, "artifacts/"+result.AgentInput.AgentID.String()+"/"+canonicalID.String(), uploads[0])

	// The receipt already owns immutable accepted content. Replay must return
	// it before consulting current grants or persisting any freshly prepared
	// upload, even when the attempted replacement content differs.
	input.Prepared, err = f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
	require.NoError(t, err)
	input.Content = workflowPhotoContent(
		prepareWorkflowAttachment(t, ctx, f, input.Prepared, "replay:unused-photo", 1, []byte("replacement photo")),
	)
	input.Content.Blocks = json.RawMessage(`[{"type":"unsupported"},{"type":"media_ref"}]`)
	require.NoError(t, f.Store.Integrations().RevokeIntegrationTargetBinding(ctx, f.Identity.ProjectID, result.BindingID))
	// Make any attempt to persist replay uploads observable, even if a later
	// rollback would otherwise hide their transient artifact rows.
	_, err = f.Store.pool.Exec(ctx, `
CREATE FUNCTION reject_replayed_workflow_artifact() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'replay must not persist prepared artifacts';
END;
$$;
CREATE TRIGGER reject_replayed_workflow_artifact
BEFORE INSERT ON artifacts FOR EACH ROW EXECUTE FUNCTION reject_replayed_workflow_artifact();`)
	require.NoError(t, err)
	replay, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
	require.NoError(t, err)
	require.False(t, replay.CreatedInput)
	require.False(t, replay.CreatedAgent)
	require.Equal(t, result.AgentInput.ID, replay.AgentInput.ID)
	require.Equal(t, result.ChannelID, replay.ChannelID)
	require.Equal(t, result.BindingID, replay.BindingID)
	require.JSONEq(t, string(result.ContentBlocks), string(replay.ContentBlocks))
	require.Equal(t, canonicalID, requireWorkflowArtifactInput(t, ctx, f, replay, photo))
	uploads, deletions, retained = blobs.snapshot()
	require.Len(t, uploads, 2)
	require.Equal(t, []string{uploads[1]}, deletions)
	require.Equal(t, 1, retained)
	require.NoError(t, f.Store.Artifacts().FinishPreparedArtifacts(
		ctx, artifactstore.ArtifactTransactionRolledBack, input.Content.Attachments[0].Artifact,
	))
	require.ErrorIs(t, f.Store.Artifacts().FinishPreparedArtifacts(
		ctx, artifactstore.ArtifactTransactionCommitted, input.Content.Attachments[0].Artifact,
	), storeerr.ErrInvalidRequest, "replay must finalize unused preparations as rolled back")
	var artifacts, inputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM artifacts WHERE agent_id = $1`, result.AgentInput.AgentID,
	).Scan(&artifacts))
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'`, result.AgentInput.AgentID,
	).Scan(&inputs))
	require.Equal(t, 1, artifacts)
	require.Equal(t, 1, inputs)
}

func TestChannelWorkflowArtifactsMalformedInputRollsBackAndCleansBatch(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"invalid block after persistence", "duplicate ordinal", "nil attachment"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			f, blobs := newChannelWorkflowArtifactFixture(t, ctx, "workflow-media-malformed")
			input := f.event(t, ctx, "first")
			first := prepareWorkflowAttachment(t, ctx, f, input.Prepared, "first:photo:0", 1, []byte("first photo"))
			second := prepareWorkflowAttachment(t, ctx, f, input.Prepared, "first:photo:1", 2, []byte("second photo"))
			input.Content = executionstore.PreparedInputContent{
				Blocks:      json.RawMessage(`[{"type":"text","text":"hello"},{"type":"media_ref"},{"type":"media_ref"}]`),
				Attachments: []executionstore.PreparedInputAttachment{first, second},
			}
			switch scenario {
			case "invalid block after persistence":
				input.Content.Blocks = json.RawMessage(`[
					{"type":"unsupported"},{"type":"media_ref"},{"type":"media_ref"}
				]`)
			case "duplicate ordinal":
				input.Content.Attachments[1].Ordinal = 1
			case "nil attachment":
				input.Content.Attachments = append(input.Content.Attachments, executionstore.PreparedInputAttachment{Ordinal: 0})
			}
			_, err := f.Store.Execution().DeliverChannelWorkflow(ctx, input)
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			requireWorkflowArtifactRowsAbsent(t, ctx, f, input.Prepared.AgentID())
			for _, table := range []string{"integration_targets", "integration_target_bindings"} {
				var count int
				require.NoError(t, f.Store.pool.QueryRow(ctx,
					fmt.Sprintf("SELECT count(*) FROM %s WHERE integration_install_id = $1", table),
					f.Identity.IntegrationInstallID,
				).Scan(&count))
				require.Zero(t, count, table)
			}
			uploads, deletions, retained := blobs.snapshot()
			require.Len(t, uploads, 2)
			require.ElementsMatch(t, uploads, deletions, "every valid prepared upload needs rollback compensation")
			require.Zero(t, retained)
		})
	}
}

func TestChannelWorkflowArtifactsCompetingWinnerRequiresFreshPreparation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	f, blobs := newChannelWorkflowArtifactFixture(t, ctx, "workflow-media-competing")
	inputs := []executionstore.DeliverChannelWorkflowInput{f.event(t, ctx, "first"), f.event(t, ctx, "second")}
	photos := [][]byte{[]byte("first event photo"), []byte("second event photo")}
	keys := []string{"first:photo:0", "second:photo:0"}
	for i := range inputs {
		inputs[i].Content = workflowPhotoContent(
			prepareWorkflowAttachment(t, ctx, f, inputs[i].Prepared, keys[i], 1, photos[i]),
		)
	}
	require.NotEqual(t, inputs[0].Prepared.AgentID(), inputs[1].Prepared.AgentID())
	// Hold the common identity so both fully prepared events contend for the
	// same first-event transaction, without holding any lock during upload.
	blocker := integrationdb.BeginTx(t, ctx, f.Store.pool)
	require.NoError(t, dbsqlc.New(blocker).LockIntegrationWorkflowIdentity(
		ctx,
		dbsqlc.LockIntegrationWorkflowIdentityParams{
			ProjectID: f.Identity.ProjectID, IntegrationInstallID: f.Identity.IntegrationInstallID,
			IntegrationRouteID: f.Identity.IntegrationRouteID, InstanceKey: f.Identity.InstanceKey,
		},
	))
	results := make([]executionstore.ChannelInputResult, 2)
	errs := make([]error, 2)
	var workers sync.WaitGroup
	for i := range inputs {
		workers.Go(func() { results[i], errs[i] = f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[i]) })
	}
	integrationdb.WaitForNamedLockWaiters(t, ctx, f.Store.pool, "LockIntegrationWorkflowIdentity", 2)
	require.NoError(t, blocker.Commit(ctx))
	workers.Wait()
	winner := -1
	for i := range inputs {
		if errs[i] == nil {
			require.Equal(t, -1, winner)
			winner = i
		} else {
			require.ErrorIs(t, errs[i], executionstore.ErrChannelWorkflowAgentChanged)
		}
	}
	require.NotEqual(t, -1, winner)
	loser := 1 - winner
	winningID := results[winner].AgentInput.AgentID
	losingID := inputs[loser].Prepared.AgentID()
	requireWorkflowArtifactInput(t, ctx, f, results[winner], photos[winner])
	requireWorkflowArtifactRowsAbsent(t, ctx, f, losingID)
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 2)
	require.Equal(t, []string{uploads[loser]}, deletions)
	require.Equal(t, 1, retained)

	// No provisional upload can be retargeted or reused after compensation.
	prepared, err := f.Store.Execution().PrepareChannelWorkflow(ctx, f.Identity)
	require.NoError(t, err)
	require.Equal(t, winningID, prepared.AgentID())
	inputs[loser].Prepared = prepared
	_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[loser])
	require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	_, deletions, retained = blobs.snapshot()
	require.Equal(t, []string{uploads[loser]}, deletions, "cleanup remains idempotent")
	require.Equal(t, 1, retained)
	inputs[loser].Content = workflowPhotoContent(
		prepareWorkflowAttachment(t, ctx, f, prepared, keys[loser], 1, photos[loser]),
	)
	results[loser], err = f.Store.Execution().DeliverChannelWorkflow(ctx, inputs[loser])
	require.NoError(t, err)
	require.False(t, results[loser].CreatedAgent)
	require.True(t, results[loser].CreatedInput)
	require.Equal(t, winningID, results[loser].AgentInput.AgentID)
	require.NotEqual(t, results[winner].AgentInput.ID, results[loser].AgentInput.ID)
	requireWorkflowArtifactInput(t, ctx, f, results[loser], photos[loser])
	uploads, deletions, retained = blobs.snapshot()
	require.Len(t, uploads, 3)
	require.Contains(t, uploads[2], "/"+winningID.String()+"/")
	require.NotContains(t, uploads[2], losingID.String())
	require.Equal(t, []string{uploads[loser]}, deletions)
	require.Equal(t, 2, retained)
	var workflows, artifacts, contentInputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx, `SELECT count(*) FROM integration_workflows`).Scan(&workflows))
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM artifacts WHERE agent_id = $1`, winningID,
	).Scan(&artifacts))
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'`, winningID,
	).Scan(&contentInputs))
	require.Equal(t, 1, workflows)
	require.Equal(t, 2, artifacts)
	require.Equal(t, 2, contentInputs)
}

func TestChannelWorkflowArtifactsCommitFailureRetainsWholeBatch(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f, blobs := newChannelWorkflowArtifactFixture(t, ctx, "workflow-media-unknown")
	first := f.event(t, ctx, "first")
	photo := []byte("existing canonical photo")
	first.Content = workflowPhotoContent(prepareWorkflowAttachment(t, ctx, f, first.Prepared, "first:photo:0", 1, photo))
	canonical, err := f.Store.Execution().DeliverChannelWorkflow(ctx, first)
	require.NoError(t, err)
	canonicalID := requireWorkflowArtifactInput(t, ctx, f, canonical, photo)
	second := f.event(t, ctx, "second")
	second.Content = executionstore.PreparedInputContent{
		Blocks: json.RawMessage(`[{"type":"text","text":"hello"},{"type":"media_ref"},{"type":"media_ref"}]`),
		Attachments: []executionstore.PreparedInputAttachment{
			prepareWorkflowAttachment(t, ctx, f, second.Prepared, "first:photo:0", 1, photo),
			prepareWorkflowAttachment(t, ctx, f, second.Prepared, "second:photo:0", 2, []byte("new photo")),
		},
	}
	// Exercise the actual COMMIT-error branch after attachment/input writes.
	// The API conservatively classifies every COMMIT error as unknown, even
	// though this injected server exception is observable as a rollback here.
	_, err = f.Store.pool.Exec(ctx, `
CREATE FUNCTION fail_workflow_artifact_commit() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'injected workflow artifact commit failure';
END;
$$;
CREATE CONSTRAINT TRIGGER fail_workflow_artifact_commit
AFTER INSERT ON artifacts DEFERRABLE INITIALLY DEFERRED
FOR EACH ROW EXECUTE FUNCTION fail_workflow_artifact_commit();`)
	require.NoError(t, err)
	_, err = f.Store.Execution().DeliverChannelWorkflow(ctx, second)
	require.ErrorContains(t, err, "commit deliver channel workflow input")
	require.ErrorContains(t, err, "injected workflow artifact commit failure")
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 3)
	require.Empty(t, deletions, "unknown outcome must retain even the unused canonical-replay upload")
	require.Equal(t, 3, retained)
	batch := []*artifactstore.PreparedArtifact{
		second.Content.Attachments[0].Artifact, second.Content.Attachments[1].Artifact,
	}
	require.NoError(t,
		f.Store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionUnknown, batch...),
	)
	require.ErrorIs(t,
		f.Store.Artifacts().FinishPreparedArtifacts(ctx, artifactstore.ArtifactTransactionRolledBack, batch...),
		storeerr.ErrInvalidRequest,
	)
	require.Equal(t, canonicalID, requireWorkflowArtifactInput(t, ctx, f, canonical, photo))
	var artifacts, contentInputs int
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM artifacts WHERE agent_id = $1`, canonical.AgentInput.AgentID,
	).Scan(&artifacts))
	require.NoError(t, f.Store.pool.QueryRow(ctx,
		`SELECT count(*) FROM agent_inputs WHERE agent_id = $1 AND input_kind = 'content'`, canonical.AgentInput.AgentID,
	).Scan(&contentInputs))
	require.Equal(t, 1, artifacts)
	require.Equal(t, 1, contentInputs)
}
