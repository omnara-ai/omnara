package apps

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type cleanupCountingInbox struct {
	appPlanStore
	reads int
}

func (s *cleanupCountingInbox) GetAppInbox(
	ctx context.Context, projectID, receiptID uuid.UUID,
) (appstore.AppInboxRecord, error) {
	s.reads++
	return s.appPlanStore.GetAppInbox(ctx, projectID, receiptID)
}

type cleanupReplayExecution struct{ AppExecutionStore }

func (cleanupReplayExecution) AdmitInboxInputSlot(
	context.Context, appstore.AppInboxLease, string,
) (executionstore.InboxInputResult, error) {
	return executionstore.InboxInputResult{Skipped: executionstore.InboxInputSkipAgentArchived}, nil
}

func TestAppInboxConsumerCleanupReadsOnlyForPlannedArtifacts(t *testing.T) {
	for _, scenario := range []string{"empty", "text only", "file"} {
		t.Run(scenario, func(t *testing.T) {
			projectID, receiptID := uuid.New(), uuid.New()
			plan := AppInboxPlan{}
			if scenario != "empty" {
				slot := AppInboxSlot{AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{}}
				if scenario == "file" {
					slot.ArtifactIDs = []uuid.UUID{uuid.New()}
				}
				plan["slot"] = slot
			}
			raw, err := json.Marshal(plan)
			require.NoError(t, err)
			inbox := &cleanupCountingInbox{appPlanStore: appPlanStore{
				receipt: appstore.AppInboxRecord{
					ID: receiptID, ProjectID: projectID, State: appstore.AppInboxCompleted,
					Plan: raw, Progress: json.RawMessage(`{"slot":{"committed":{"skipped":"agent_archived"}}}`),
				},
			}}
			artifacts := &failedInboxArtifacts{}
			router := NewAppRouter(cleanupReplayExecution{}, inbox)
			consumer := NewAppInboxConsumer(router, inbox, artifacts, nil, nil, nil)
			_, err = consumer.Consume(t.Context(), appstore.AppInboxLease{
				ProjectID: projectID, ReceiptID: receiptID, Token: uuid.New(),
			})
			require.NoError(t, err)
			if scenario == "file" {
				require.Equal(t, 3, inbox.reads, "completed replay still attempts unused-upload cleanup")
				require.Equal(t, plan["slot"].ArtifactIDs, artifacts.deleted)
			} else {
				require.Equal(t, 2, inbox.reads, "only consumer and router reads; no cleanup read")
				require.Empty(t, artifacts.deleted)
			}
		})
	}
}

func TestAppInboxCleanupRequiresTerminalReceiptAndProtectsDelivery(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		state       appstore.AppInboxState
		progress    string
		wantDeleted bool
		wantError   bool
	}{
		{"pending", appstore.AppInboxPending, `{}`, false, false},
		{
			"processing with skip", appstore.AppInboxProcessing,
			`{"a":{"committed":{"skipped":"agent_archived"}}}`, false, false,
		},
		{
			"completed skip", appstore.AppInboxCompleted,
			`{"a":{"committed":{"skipped":"agent_archived"}},"b":{"committed":{"skipped":"agent_archived"}}}`, true, false,
		},
		{
			"completed missing settlement", appstore.AppInboxCompleted,
			`{"a":{"committed":{"skipped":"agent_archived"}}}`, false, true,
		},
		{
			"unknown outcome protected", appstore.AppInboxFailed,
			`{"a":{"committed":{"skipped":"unknown"}},"b":{"committed":{}}}`, false, false,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			projectID, receiptID, agentID := uuid.New(), uuid.New(), uuid.New()
			artifactIDs := []uuid.UUID{uuid.New(), uuid.New()}
			slot := AppInboxSlot{
				AgentID: agentID, ArtifactIDs: artifactIDs[:1],
				Input: &executionstore.CreateAgentContentInputInput{},
			}
			otherSlot := slot
			otherSlot.ArtifactIDs = artifactIDs[1:]
			plan, err := json.Marshal(AppInboxPlan{"a": slot, "b": otherSlot})
			require.NoError(t, err)
			receipt := appstore.AppInboxRecord{
				ID: receiptID, ProjectID: projectID, State: scenario.state,
				Plan: plan, Progress: json.RawMessage(scenario.progress),
			}
			inbox := &appPlanStore{receipt: receipt}
			artifacts := &failedInboxArtifacts{}
			err = CleanupTerminalAppInboxArtifacts(t.Context(), inbox, artifacts, projectID, receiptID)
			if scenario.wantError {
				require.ErrorIs(t, err, storeerr.ErrStateTransitionConflict)
			} else {
				require.NoError(t, err)
			}
			if scenario.wantDeleted {
				require.ElementsMatch(t, artifactIDs, artifacts.deleted)
			} else {
				require.Empty(t, artifacts.deleted)
			}
			require.Equal(t, receipt, inbox.receipt)
		})
	}
}
