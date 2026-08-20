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

	if _, err := pool.Exec(ctx, `ALTER TABLE cron_triggers DROP CONSTRAINT cron_triggers_name_policy`); err != nil {
		t.Fatalf("drop cron trigger name constraint: %v", err)
	}
	const invalidStoredName = " invalid trigger "
	if _, err := pool.Exec(
		ctx,
		`UPDATE cron_triggers SET name = $1 WHERE id = $2`,
		invalidStoredName,
		created.ID,
	); err != nil {
		t.Fatalf("seed invalid cron trigger name: %v", err)
	}

	disabled := false
	if _, err := store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID,
		TriggerID: created.ID,
		Enabled:   &disabled,
	}); !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("update with invalid stored cron trigger name error = %v, want invalid request", err)
	}
	repairedName := "Repaired 🚀 Trigger"
	repaired, err := store.Execution().UpdateCronTrigger(ctx, executionstore.UpdateCronTriggerInput{
		ProjectID: testProjectID,
		TriggerID: created.ID,
		Name:      &repairedName,
	})
	if err != nil {
		t.Fatalf("repair cron trigger name: %v", err)
	}
	if repaired.Name != repairedName {
		t.Fatalf("repaired cron trigger name = %q, want %q", repaired.Name, repairedName)
	}
}
