package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
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
	context.Context, integrationstore.IntegrationInboxLease, string, []artifactstore.PreparedArtifact,
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
					Plan: raw,
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
				require.Equal(t, plan["slot"].ArtifactIDs, artifacts.checked)
			} else {
				require.Equal(t, 2, inbox.reads, "only consumer and router reads; no cleanup read")
				require.Empty(t, artifacts.checked)
			}
		})
	}
}

func TestIntegrationInboxCleanupChecksAllPlannedArtifactsOnlyAfterTerminalReceipt(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		state       integrationstore.IntegrationInboxState
		wantChecked bool
	}{
		{"pending", integrationstore.IntegrationInboxPending, false},
		{"processing", integrationstore.IntegrationInboxProcessing, false},
		{"completed", integrationstore.IntegrationInboxCompleted, true},
		{"failed", integrationstore.IntegrationInboxFailed, true},
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
				Plan: plan,
			}
			inbox := &integrationPlanStore{receipt: receipt}
			artifacts := &failedInboxArtifacts{}
			err = CleanupTerminalIntegrationInboxArtifacts(t.Context(), inbox, artifacts, projectID, receiptID)
			require.NoError(t, err)
			if scenario.wantChecked {
				require.ElementsMatch(t, artifactIDs, artifacts.checked)
			} else {
				require.Empty(t, artifacts.checked)
			}
			require.Equal(t, receipt, inbox.receipt)
		})
	}
}
