package executionstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestInteractionSelectionBackgroundActorIdentity(t *testing.T) {
	t.Parallel()
	for _, kind := range []publicid.Kind{
		publicid.KindAgent, publicid.KindCronTrigger, publicid.KindUser, publicid.KindOrgAPIKey,
	} {
		t.Run(string(kind), func(t *testing.T) {
			t.Parallel()
			id, err := publicid.Encode(kind, uuid.New())
			require.NoError(t, err)
			require.Equal(t, kind == publicid.KindAgent || kind == publicid.KindCronTrigger,
				internalInputPreservesInteractionSelection(ActorProviderOmnara, id))
			require.False(t, internalInputPreservesInteractionSelection(ActorProviderExternal, id))
			require.False(t, internalInputPreservesInteractionSelection(ActorProviderIntegration, id))
			require.False(t, internalInputPreservesInteractionSelection(ActorProviderOmnara, id[:len(id)-1]))
		})
	}
	require.False(t, internalInputPreservesInteractionSelection(ActorProviderOmnara, "agt_aaaaaaaaaaaaaaaaaaaaaaaaaa"))
	require.False(t, internalInputPreservesInteractionSelection(ActorProviderOmnara, "cron_aaaaaaaaaaaaaaaaaaaaaaaaaa"))
}
