package integrationstore

import (
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func TestClaimNextIntegrationEventRejectsInvalidLeaseBeforeDatabase(t *testing.T) {
	t.Parallel()
	store := New(nil, nil)
	for _, duration := range []time.Duration{
		-time.Second, 0, time.Nanosecond, time.Microsecond, 999 * time.Microsecond,
		time.Millisecond - time.Nanosecond, 5*time.Minute + time.Nanosecond,
	} {
		t.Run(duration.String(), func(t *testing.T) {
			t.Parallel()
			receipt, found, err := store.ClaimNextIntegrationEvent(t.Context(), ClaimNextIntegrationEventInput{
				Capability:    channelconnector.Capability{ConnectorKey: "test_connector", Provider: "discord"},
				LeaseDuration: duration,
			})
			require.ErrorIs(t, err, storeerr.ErrInvalidRequest)
			require.False(t, found)
			require.Zero(t, receipt)
		})
	}
}
