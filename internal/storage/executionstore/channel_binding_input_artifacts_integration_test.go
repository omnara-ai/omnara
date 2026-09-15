//go:build integration

package executionstore_test

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func (f boundChannelInputFixture) media(t *testing.T, agentID ID, key string) executionstore.PreparedInputContent {
	t.Helper()
	prepared, err := f.store.Artifacts().PrepareArtifact(t.Context(), artifactstore.CreateArtifactInput{
		ProjectID: testProjectID, AgentID: agentID, ContentType: "image/png", Filename: "photo.png",
		Content: []byte("bound input image bytes"), MaxBytes: 1024, IdempotencyKey: key,
	})
	require.NoError(t, err)
	return executionstore.PreparedInputContent{
		Blocks: json.RawMessage(`[{"type":"text","text":"hello"},{"type":"media_ref"}]`),
		Attachments: []executionstore.PreparedInputAttachment{{
			Ordinal: 1, Artifact: prepared, Metadata: json.RawMessage(`{"provider_filename":"photo.png"}`),
		}},
	}
}

func TestBoundChannelInputArtifactCommitAndUnusedReplayCleanup(t *testing.T) {
	t.Parallel()
	blobs := &workflowArtifactBlobs{content: make(map[string][]byte)}
	f := newBoundChannelInputFixture(t, WithBlobStore(blobs))
	input := f.event(t, "media")
	before := f.rows(t)
	input.Content = f.media(t, input.Prepared.AgentID(), "original-upload")
	require.Equal(t, before, f.rows(t), "upload preparation leaves no artifact or input metadata")
	accepted, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	require.True(t, accepted.CreatedInput)
	var blocks []struct {
		Type       string          `json:"type"`
		ArtifactID string          `json:"artifact_id"`
		Metadata   json.RawMessage `json:"metadata"`
	}
	require.NoError(t, json.Unmarshal(accepted.ContentBlocks, &blocks))
	require.Len(t, blocks, 2)
	require.Equal(t, "media_ref", blocks[1].Type)
	require.JSONEq(t, `{"provider_filename":"photo.png"}`, string(blocks[1].Metadata))
	artifactID, err := ParseID(blocks[1].ArtifactID)
	require.NoError(t, err)
	content, record, err := f.store.Artifacts().GetArtifactBlob(t.Context(), testProjectID, f.binding.AgentID, artifactID)
	require.NoError(t, err)
	require.Equal(t, []byte("bound input image bytes"), content)
	require.Equal(t, blobstore.ContentDigest(content), record.Digest)
	require.Equal(t, "photo.png", record.Filename)
	require.Equal(t, f.binding.AgentID, record.AgentID)
	require.NoError(t, f.store.Integrations().RevokeIntegrationTargetBinding(t.Context(), testProjectID, f.binding.ID))
	before = f.rows(t)
	replay := f.event(t, "media-receipt-replay")
	replay.InputKey = input.InputKey
	replay.Content = f.media(t, replay.Prepared.AgentID(), "unused-replay-upload")
	got, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), replay)
	require.NoError(t, err)
	require.False(t, got.CreatedInput)
	require.Equal(t, accepted.AgentInput.ID, got.AgentInput.ID)
	require.JSONEq(t, string(accepted.ContentBlocks), string(got.ContentBlocks))
	before["integration_event_outcomes"]++
	require.Equal(t, before, f.rows(t))
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 2)
	require.Equal(t, []string{uploads[1]}, deletions, "unused replay upload is compensated, accepted artifact survives")
	require.Equal(t, 1, retained)
}

func TestBoundChannelInputLateFailureRollsBackArtifactsInputActorAndOutcome(t *testing.T) {
	t.Parallel()
	blobs := &workflowArtifactBlobs{content: make(map[string][]byte)}
	f := newBoundChannelInputFixture(t, WithBlobStore(blobs))
	input := f.event(t, "late-failure")
	input.Content = f.media(t, input.Prepared.AgentID(), "failed-upload")
	before := f.rows(t)
	removeFailure := f.rejectOutcome(t)
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.ErrorContains(t, err, "bound input outcome failure")
	require.Equal(t, before, f.rows(t), "a late failure must roll back all admission effects on the existing agent")
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 1)
	require.Equal(t, uploads, deletions)
	require.Zero(t, retained)
	removeFailure()
	// Reprepare the consumed upload and retry the same lease/key; no phantom
	// input or outcome from the failed transaction may suppress this admission.
	input.Content = f.media(t, input.Prepared.AgentID(), "retry-upload")
	accepted, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.NoError(t, err)
	require.True(t, accepted.CreatedInput)
	after := f.rows(t)
	require.Equal(t, before["artifacts"]+1, after["artifacts"])
	require.Equal(t, before["agent_inputs"]+1, after["agent_inputs"])
	require.Equal(t, before["integration_event_outcomes"]+1, after["integration_event_outcomes"])
}

func TestBoundChannelInputRejectsAnotherAgentsPreparedArtifact(t *testing.T) {
	t.Parallel()
	blobs := &workflowArtifactBlobs{content: make(map[string][]byte)}
	f := newBoundChannelInputFixture(t, WithBlobStore(blobs))
	otherAgent := mustCreateAgent(t, t.Context(), f.store)
	input := f.event(t, "wrong-artifact-owner")
	input.Content = f.media(t, otherAgent, "foreign-upload")
	before := f.rows(t)
	_, err := f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.Equal(t, before, f.rows(t))
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 1)
	require.Equal(t, uploads, deletions)
	require.Zero(t, retained)
}

func TestBoundChannelInputLeaseExpiryAtOutcomeRollsBackPreparedMedia(t *testing.T) {
	t.Parallel()
	blobs := &workflowArtifactBlobs{content: make(map[string][]byte)}
	f := newBoundChannelInputFixture(t, WithBlobStore(blobs))
	input := f.event(t, "expires-before-commit")
	input.Content = f.media(t, input.Prepared.AgentID(), "expires-upload")
	before := f.rows(t)
	// Deterministically advance the lease to expired after writing the input.
	// This targets the final lease fence, without sleeps or racing wall clocks.
	_, err := f.store.pool.Exec(t.Context(), `
CREATE FUNCTION expire_bound_input_lease() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  UPDATE integration_event_receipts SET lease_expires_at = now() - interval '1 second' WHERE id = NEW.receipt_id;
  RETURN NEW;
END;
$$;
CREATE TRIGGER expire_bound_input_lease BEFORE INSERT ON integration_event_outcomes
FOR EACH ROW EXECUTE FUNCTION expire_bound_input_lease();`)
	require.NoError(t, err)
	_, err = f.store.Execution().DeliverBoundChannelInput(t.Context(), input)
	require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
	require.Equal(t, before, f.rows(t))
	uploads, deletions, retained := blobs.snapshot()
	require.Len(t, uploads, 1)
	require.Equal(t, uploads, deletions)
	require.Zero(t, retained)
}
