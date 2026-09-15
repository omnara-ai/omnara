package integrationstore

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestIntegrationEventMaintenanceRejectsInvalidBoundsBeforeDB(t *testing.T) {
	store := &Store{}
	for _, limit := range []int{0, -1, 1001} {
		_, err := store.FailUnprocessableIntegrationEvents(t.Context(), limit)
		require.Error(t, err)
		_, err = store.DeleteRetainedIntegrationEvents(t.Context(), DeleteRetainedIntegrationEventsInput{
			Limit: limit, Retention: 7 * 24 * time.Hour,
		})
		require.Error(t, err)
	}
	for _, retention := range []time.Duration{0, -time.Second, time.Nanosecond} {
		_, err := store.DeleteRetainedIntegrationEvents(t.Context(), DeleteRetainedIntegrationEventsInput{
			Limit: 1, Retention: retention,
		})
		require.Error(t, err)
	}
}
