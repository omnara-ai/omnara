//go:build integration

package executionstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestCronTriggerAdmissionSerializesWithProjectDeletion(t *testing.T) {
	t.Parallel()
	t.Run("creation wins", func(t *testing.T) {
		ctx := context.Background()
		fixture := newMachineLifecycleLockOrderFixture(t, ctx, "cron-create-wins")
		actor := scopeDeletionActor(t, fixture)

		controlTx, err := fixture.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin cron creation control transaction: %v", err)
		}
		defer func() { _ = controlTx.Rollback(ctx) }()
		if err := dbsqlc.New(controlTx).LockResourceCreation(
			ctx,
			dbsqlc.LockResourceCreationParams{
				ResourceKind: "cron_triggers",
				Scope:        testProjectID.String(),
			},
		); err != nil {
			t.Fatalf("lock cron trigger creation: %v", err)
		}

		type createOutcome struct {
			record executionstore.CronTriggerRecord
			err    error
		}
		createDone := make(chan createOutcome, 1)
		go func() {
			record, createErr := fixture.store.Execution().CreateCronTrigger(
				context.Background(),
				cronTriggerInput("Cron Before Deletion", fixture.agent.ID, true),
			)
			createDone <- createOutcome{record: record, err: createErr}
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockResourceCreation", 1)

		deleteDone := make(chan error, 1)
		go func() {
			_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
				context.Background(),
				testOrgID,
				testProjectID,
				actor,
			)
			deleteDone <- deleteErr
		}()
		integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleExclusive", 1)
		if err := controlTx.Commit(ctx); err != nil {
			t.Fatalf("release cron creation control transaction: %v", err)
		}

		var created executionstore.CronTriggerRecord
		select {
		case outcome := <-createDone:
			if outcome.err != nil {
				t.Fatalf("create cron trigger before project deletion: %v", outcome.err)
			}
			created = outcome.record
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for cron trigger creation")
		}
		select {
		case err := <-deleteDone:
			if err != nil {
				t.Fatalf("delete project after cron trigger creation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for project deletion")
		}

		var activeCount, deletedCount int
		if err := fixture.pool.QueryRow(
			ctx,
			`SELECT
			   count(*) FILTER (WHERE deleted_at IS NULL)::integer,
			   count(*) FILTER (WHERE deleted_at IS NOT NULL)::integer
			 FROM cron_triggers
			 WHERE id = $1`,
			created.ID,
		).Scan(&activeCount, &deletedCount); err != nil {
			t.Fatalf("count cron trigger after project deletion: %v", err)
		}
		if activeCount != 0 || deletedCount != 1 {
			t.Fatalf("cron trigger rows after deletion: active=%d deleted=%d", activeCount, deletedCount)
		}
	})

	for _, operation := range []string{"create", "enable"} {
		t.Run("deletion wins "+operation, func(t *testing.T) {
			ctx := context.Background()
			fixture := newMachineLifecycleLockOrderFixture(t, ctx, "cron-delete-wins-"+operation)
			actor := scopeDeletionActor(t, fixture)

			var existing executionstore.CronTriggerRecord
			if operation == "enable" {
				var err error
				existing, err = fixture.store.Execution().CreateCronTrigger(
					ctx,
					cronTriggerInput("Cron Rejected Enable", fixture.agent.ID, false),
				)
				if err != nil {
					t.Fatalf("create disabled cron trigger: %v", err)
				}
			}

			controlTx, err := fixture.pool.Begin(ctx)
			if err != nil {
				t.Fatalf("begin project deletion control transaction: %v", err)
			}
			defer func() { _ = controlTx.Rollback(ctx) }()
			if _, err := dbsqlc.New(controlTx).LockAgentInProject(
				ctx,
				dbsqlc.LockAgentInProjectParams{ProjectID: testProjectID, ID: fixture.agent.ID},
			); err != nil {
				t.Fatalf("lock project agent: %v", err)
			}

			deleteDone := make(chan error, 1)
			go func() {
				_, deleteErr := fixture.store.Organizations().DeleteProjectOnceForIntegration(
					context.Background(),
					testOrgID,
					testProjectID,
					actor,
				)
				deleteDone <- deleteErr
			}()
			integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockAgentInProject", 1)

			admissionDone := make(chan error, 1)
			go func() {
				if operation == "create" {
					_, createErr := fixture.store.Execution().CreateCronTrigger(
						context.Background(),
						cronTriggerInput("Cron Rejected Create", fixture.agent.ID, true),
					)
					admissionDone <- createErr
					return
				}
				enabled := true
				_, updateErr := fixture.store.Execution().UpdateCronTrigger(
					context.Background(),
					executionstore.UpdateCronTriggerInput{
						ProjectID: testProjectID,
						TriggerID: existing.ID,
						Enabled:   &enabled,
					},
				)
				admissionDone <- updateErr
			}()
			integrationdb.WaitForNamedLockWaiters(t, ctx, fixture.pool, "LockProjectLifecycleShared", 1)
			if err := controlTx.Commit(ctx); err != nil {
				t.Fatalf("release project deletion control transaction: %v", err)
			}

			select {
			case err := <-deleteDone:
				if err != nil {
					t.Fatalf("delete project before cron trigger %s: %v", operation, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timed out waiting for project deletion")
			}
			select {
			case err := <-admissionDone:
				if !errors.Is(err, storeerr.ErrNotFound) {
					t.Fatalf("cron trigger %s after deletion error = %v, want not found", operation, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("timed out waiting for rejected cron trigger %s", operation)
			}

			if operation == "create" {
				var count int
				if err := fixture.pool.QueryRow(
					ctx,
					`SELECT count(*)::integer FROM cron_triggers WHERE project_id = $1 AND name = $2`,
					testProjectID,
					"Cron Rejected Create",
				).Scan(&count); err != nil {
					t.Fatalf("count rejected cron trigger: %v", err)
				}
				if count != 0 {
					t.Fatalf("rejected cron trigger rows = %d, want zero", count)
				}
				return
			}

			var enabled bool
			var deletedAt *time.Time
			if err := fixture.pool.QueryRow(
				ctx,
				`SELECT enabled, deleted_at FROM cron_triggers WHERE id = $1`,
				existing.ID,
			).Scan(&enabled, &deletedAt); err != nil {
				t.Fatalf("load rejected cron trigger enable: %v", err)
			}
			if enabled || deletedAt == nil {
				t.Fatalf("cron trigger after rejected enable: enabled=%t deleted_at=%v", enabled, deletedAt)
			}
		})
	}
}

func cronTriggerInput(name string, agentID ID, enabled bool) executionstore.CreateCronTriggerInput {
	return executionstore.CreateCronTriggerInput{
		ProjectID: testProjectID,
		Name:      name,
		Target: executionstore.CronTriggerTarget{
			Kind: executionstore.CronTriggerTargetAgent,
			ID:   agentID,
		},
		CronExpression:  "0 9 * * *",
		Timezone:        "UTC",
		MessageTemplate: "Run the scheduled task.",
		Enabled:         enabled,
		IdempotencyKey:  name,
	}
}
