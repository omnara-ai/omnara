package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type cleanupCountingInbox struct {
	integrationPlanStore
	reads int
}

func (s *cleanupCountingInbox) GetIntegrationInbox(
	ctx context.Context, projectID, receiptID uuid.UUID,
) (integrationstore.IntegrationInboxRecord, error) {
	s.reads++
	return s.integrationPlanStore.GetIntegrationInbox(ctx, projectID, receiptID)
}

type cleanupReplayExecution struct{ IntegrationExecutionStore }

func (cleanupReplayExecution) AdmitInboxInputSlot(
	context.Context, integrationstore.IntegrationInboxLease, string,
) (executionstore.InboxInputResult, error) {
	return executionstore.InboxInputResult{Skipped: executionstore.InboxInputSkipAgentArchived}, nil
}

func TestIntegrationInboxConsumerCleanupReadsOnlyForPlannedArtifacts(t *testing.T) {
	for _, scenario := range []string{"empty", "text only", "file"} {
		t.Run(scenario, func(t *testing.T) {
			projectID, receiptID := uuid.New(), uuid.New()
			plan := IntegrationInboxPlan{}
			if scenario != "empty" {
				slot := IntegrationInboxSlot{AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{}}
				if scenario == "file" {
					slot.ArtifactIDs = []uuid.UUID{uuid.New()}
				}
				plan["slot"] = slot
			}
			raw, err := json.Marshal(plan)
			require.NoError(t, err)
			inbox := &cleanupCountingInbox{integrationPlanStore: integrationPlanStore{
				receipt: integrationstore.IntegrationInboxRecord{
					ID: receiptID, ProjectID: projectID, State: integrationstore.IntegrationInboxCompleted,
					Plan: raw, Progress: json.RawMessage(`{"slot":{"committed":{"skipped":"agent_archived"}}}`),
				},
			}}
			artifacts := &failedInboxArtifacts{}
			router := NewIntegrationRouter(cleanupReplayExecution{}, inbox)
			consumer := NewIntegrationInboxConsumer(router, inbox, artifacts, nil, nil, nil)
			_, err = consumer.Consume(t.Context(), integrationstore.IntegrationInboxLease{
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

func TestIntegrationInboxCleanupRequiresTerminalReceiptAndProtectsDelivery(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		state       integrationstore.IntegrationInboxState
		progress    string
		wantDeleted bool
		wantError   bool
	}{
		{"pending", integrationstore.IntegrationInboxPending, `{}`, false, false},
		{
			"processing with skip", integrationstore.IntegrationInboxProcessing,
			`{"a":{"committed":{"skipped":"agent_archived"}}}`, false, false,
		},
		{
			"completed skip", integrationstore.IntegrationInboxCompleted,
			`{"a":{"committed":{"skipped":"agent_archived"}},"b":{"committed":{"skipped":"agent_archived"}}}`, true, false,
		},
		{
			"completed missing settlement", integrationstore.IntegrationInboxCompleted,
			`{"a":{"committed":{"skipped":"agent_archived"}}}`, false, true,
		},
		{
			"unknown outcome protected", integrationstore.IntegrationInboxFailed,
			`{"a":{"committed":{"skipped":"unknown"}},"b":{"committed":{}}}`, false, false,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			projectID, receiptID, agentID := uuid.New(), uuid.New(), uuid.New()
			artifactIDs := []uuid.UUID{uuid.New(), uuid.New()}
			slot := IntegrationInboxSlot{
				AgentID: agentID, ArtifactIDs: artifactIDs[:1],
				Input: &executionstore.CreateAgentContentInputInput{},
			}
			otherSlot := slot
			otherSlot.ArtifactIDs = artifactIDs[1:]
			plan, err := json.Marshal(IntegrationInboxPlan{"a": slot, "b": otherSlot})
			require.NoError(t, err)
			receipt := integrationstore.IntegrationInboxRecord{
				ID: receiptID, ProjectID: projectID, State: scenario.state,
				Plan: plan, Progress: json.RawMessage(scenario.progress),
			}
			inbox := &integrationPlanStore{receipt: receipt}
			artifacts := &failedInboxArtifacts{}
			err = CleanupTerminalIntegrationInboxArtifacts(t.Context(), inbox, artifacts, projectID, receiptID)
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
