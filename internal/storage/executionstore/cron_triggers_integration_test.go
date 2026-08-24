//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestCronTriggerNamesUseResourceNamePolicy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	pool := openIntegrationDB(t, ctx)
	seedMigratedDB(t, ctx, pool)
	store := newIntegrationStore(pool)
	profile := createLaunchTestAgent(t, ctx, store, "cron-trigger-name-profile", testAgentConfigYAML())

	base := executionstore.CreateCronTriggerInput{
		ProjectID:       testProjectID,
		Name:            " Nightly Review ",
		Target:          executionstore.CronTriggerTarget{Kind: executionstore.CronTriggerTargetAgentProfile, ID: profile.ID},
		CronExpression:  "0 9 * * *",
		Timezone:        "UTC",
		MessageTemplate: "Review the queue.",
		Enabled:         true,
		IdempotencyKey:  "cron-trigger-invalid-name",
	}
	if _, err := store.Execution().CreateCronTrigger(ctx, base); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("create cron trigger with boundary whitespace error = %v, want invalid request", err)
	}

	base.Name = "Nightly 🚀 Review"
	base.IdempotencyKey = "cron-trigger-valid-name"
	created, err := store.Execution().CreateCronTrigger(ctx, base)
	if err != nil {
		t.Fatalf("create cron trigger: %v", err)
	}
	if created.Name != base.Name {
		t.Fatalf("created cron trigger name = %q, want exact input %q", created.Name, base.Name)
	}

	invalidName := " invalid trigger "
	if _, err := store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID,
		TriggerID: created.ID,
		Name:      &invalidName,
	}); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("update cron trigger with invalid name error = %v, want invalid request", err)
	}
}
