package maintenance

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	runtimeLockReapBatchSize     int32 = 100
	eventWebhookCleanupBatchSize int32 = 500
	eventWebhookCleanupTimeout         = 5 * time.Second
)

type CoreResult struct {
	ReapedRuntimeLocks            int64
	ReapRuntimeLocksErr           error
	ExpiredDaemonRuntimes         int
	ExpireDaemonRuntimesErr       error
	ExpiredProcessTools           int64
	ExpireProcessToolsErr         error
	DeletedWebhooks               int64
	WebhookCleanupErr             error
	AuthCleanup                   identitystore.AuthStateCleanupResult
	AuthCleanupErr                error
	DeletedIntegrationStates      int64
	IntegrationStatesCleanupErr   error
	CompletedInbox                int64
	CompletedInboxBudgetExhausted bool
	CompletedInboxCleanupErr      error
	DeletedInbox                  int64
	DeletedInboxBudgetExhausted   bool
	DeletedInboxCleanupErr        error
}

func RunCore(ctx context.Context, store *storage.Store) CoreResult {
	var result CoreResult
	var tasks sync.WaitGroup
	tasks.Go(func() {
		defer recoverMaintenanceTask("reap runtime locks", &result.ReapRuntimeLocksErr)
		result.ReapedRuntimeLocks, result.ReapRuntimeLocksErr = store.Execution().ReapExpiredAgentRuntimeLocks(
			ctx, runtimeLockReapBatchSize,
		)
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("expire daemon runtimes", &result.ExpireDaemonRuntimesErr)
		records, err := store.Execution().EndExpiredDaemonRuntimes(ctx, 100)
		result.ExpireDaemonRuntimesErr = err
		if err == nil {
			result.ExpiredDaemonRuntimes = len(records)
		}
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("expire process tools", &result.ExpireProcessToolsErr)
		result.ExpiredProcessTools, result.ExpireProcessToolsErr = store.Execution().ExpireProcessToolCallsForAllProjects(
			ctx, executionstore.ProcessToolMachineUnreachableGrace,
		)
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("cleanup event webhooks", &result.WebhookCleanupErr)
		cleanupCtx, cancel := context.WithTimeout(ctx, eventWebhookCleanupTimeout)
		defer cancel()
		result.DeletedWebhooks, result.WebhookCleanupErr = store.Execution().DeleteExpiredEventWebhookDeliveries(
			cleanupCtx, eventWebhookCleanupBatchSize,
		)
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("cleanup auth state", &result.AuthCleanupErr)
		result.AuthCleanup, result.AuthCleanupErr = store.Identity().CleanupInactiveAuthState(ctx)
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("cleanup integration states", &result.IntegrationStatesCleanupErr)
		result.DeletedIntegrationStates, result.IntegrationStatesCleanupErr = store.Integrations().CleanupIntegrationStates(
			ctx, IntegrationInboxRetention, integrationInboxCleanupBatch,
		)
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("cleanup completed integration inbox", &result.CompletedInboxCleanupErr)
		result.CompletedInbox, result.CompletedInboxBudgetExhausted, result.CompletedInboxCleanupErr =
			drainIntegrationInboxCleanup(
				ctx, func(cleanupCtx context.Context) (int64, error) {
					return store.Integrations().CleanupTerminalIntegrationInbox(
						cleanupCtx, IntegrationInboxRetention, integrationInboxCleanupBatch,
					)
				},
			)
	})
	tasks.Go(func() {
		defer recoverMaintenanceTask("cleanup deleted integration inbox", &result.DeletedInboxCleanupErr)
		result.DeletedInbox, result.DeletedInboxBudgetExhausted, result.DeletedInboxCleanupErr = drainIntegrationInboxCleanup(
			ctx, func(cleanupCtx context.Context) (int64, error) {
				return store.Integrations().CleanupDeletedIntegrationInbox(cleanupCtx, integrationInboxCleanupBatch)
			},
		)
	})
	tasks.Wait()
	return result
}

func recoverMaintenanceTask(operation string, err *error) {
	if recovered := recover(); recovered != nil {
		*err = fmt.Errorf("%s panicked: %v\n%s", operation, recovered, debug.Stack())
	}
}
