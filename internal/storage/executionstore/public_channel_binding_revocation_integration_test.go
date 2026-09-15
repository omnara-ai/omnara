//go:build integration

package executionstore_test

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestPublicChannelBindingRevokeChecksHistoricalOwner(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	f := newPublicChannelFixture(t, ctx, "public-binding-revoke")
	bindings := f.store.Integrations()
	original, err := bindings.CreateIntegrationTargetBinding(ctx, f.grants(true))
	require.NoError(t, err)
	replacement, err := bindings.CreateIntegrationTargetBinding(ctx, f.grants(false))
	require.NoError(t, err)
	require.NotEqual(t, original.ID, replacement.ID)
	for _, id := range []uuid.UUID{original.ID, replacement.ID} {
		require.ErrorIs(t, bindings.RevokeAgentChannelBinding(ctx, testProjectID, uuid.New(), id), storeerr.ErrNotFound)
		require.ErrorIs(t, bindings.RevokeAgentChannelBinding(ctx, uuid.New(), f.agent.ID, id), storeerr.ErrNotFound)
	}
	require.ErrorIs(t, bindings.RevokeAgentChannelBinding(ctx, testProjectID, f.agent.ID, uuid.New()),
		storeerr.ErrNotFound)
	for range 2 {
		require.NoError(t, bindings.RevokeAgentChannelBinding(ctx, testProjectID, f.agent.ID, original.ID))
	}
	live, err := bindings.GetIntegrationTargetBinding(ctx, testProjectID, replacement.ID)
	require.NoError(t, err, "historical deletion cannot revoke the replacement")
	require.True(t, live.SendAllowed)
	for range 2 {
		require.NoError(t, bindings.RevokeAgentChannelBinding(ctx, testProjectID, f.agent.ID, replacement.ID))
	}
	_, err = bindings.GetIntegrationTargetBinding(ctx, testProjectID, replacement.ID)
	require.ErrorIs(t, err, storeerr.ErrNotFound, "the normal lookup remains live-only")
	require.NoError(t, bindings.DeleteIntegrationInstall(ctx, testProjectID, f.install.ID))
	for _, id := range []uuid.UUID{original.ID, replacement.ID} {
		require.NoError(t, bindings.RevokeAgentChannelBinding(ctx, testProjectID, f.agent.ID, id),
			"historical revoke does not require the connection to remain live")
	}
}
