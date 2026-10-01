package maintenance

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/stretchr/testify/require"
)

func TestRunCoreMaintenanceRecoversEachTaskPanic(t *testing.T) {
	result := RunCore(t.Context(), &storage.Store{})
	for _, err := range []error{
		result.ReapRuntimeLocksErr, result.ExpireDaemonRuntimesErr, result.ExpireProcessToolsErr,
		result.WebhookCleanupErr, result.AuthCleanupErr,
		result.IntegrationStatesCleanupErr, result.CompletedInboxCleanupErr, result.DeletedInboxCleanupErr,
	} {
		require.ErrorContains(t, err, "panicked:")
		require.ErrorContains(t, err, "goroutine")
	}
}
