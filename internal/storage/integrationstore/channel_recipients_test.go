package integrationstore

import (
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestChannelRecipientsRejectInvalidScopeBeforeDatabase(t *testing.T) {
	t.Parallel()
	store := New(nil, nil)
	ctx := t.Context()
	for i := range 3 {
		ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
		ids[i] = uuid.Nil
		_, err := store.GetChannelBindingIdentity(ctx, ids[0], ids[1], ids[2])
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		_, err = store.ListChannelReceiveBindings(ctx, ids[0], ids[1], ids[2], uuid.Nil, 1)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
		_, err = store.LookupChannelReceiptRouting(ctx, ids[0], ids[1], "thread", ids[2])
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	for _, limit := range []int32{-1, 0} {
		_, err := store.ListChannelReceiveBindings(ctx, uuid.New(), uuid.New(), uuid.New(), uuid.Nil, limit)
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
	for _, ref := range []string{"", "thread\x00"} {
		_, err := store.LookupChannelReceiptRouting(ctx, uuid.New(), uuid.New(), ref, uuid.New())
		require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
	}
}
