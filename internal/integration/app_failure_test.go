package integration

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

type failedInboxProvider struct {
	appConsumerProvider
	notices int
	message string
	err     error
}

func (p *failedInboxProvider) NotifyInboxFailure(
	_ context.Context, _ integrationstore.ProjectAppRecord, _ integrationstore.IntegrationInboxRecord, message string,
) error {
	p.notices++
	p.message = message
	return p.err
}

type failedInboxArtifacts struct {
	appConsumerUploads
	deleted []uuid.UUID
}

func (a *failedInboxArtifacts) DeleteUnreferencedPreparedArtifact(
	_ context.Context, _, _ uuid.UUID, artifact uuid.UUID,
) error {
	a.deleted = append(a.deleted, artifact)
	return nil
}

func TestAppFailureFinalizationPreservesAcceptedWork(t *testing.T) {
	for _, scenario := range []struct {
		name, progress string
		planned, empty bool
		wantNotice     bool
		wantCleanup    bool
	}{
		{"before planning", `{}`, false, false, true, false},
		{"unrouted", `{}`, true, true, false, false},
		{"complete bookkeeping failed", `{"a":{"committed":{}},"b":{"committed":{}}}`, true, false, false, false},
		{"partially admitted", `{"a":{"committed":{}}}`, true, false, true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, app, receiptID := uuid.New(), uuid.New(), uuid.New()
			unused := uuid.New()
			receipt := integrationstore.IntegrationInboxRecord{
				ID: receiptID, ProjectID: project, AppID: app,
				State: integrationstore.IntegrationInboxFailed, Progress: json.RawMessage(scenario.progress),
			}
			if scenario.planned {
				plan := AppInboxPlan{}
				if !scenario.empty {
					plan["a"] = AppInboxSlot{
						AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{},
						ArtifactIDs: []uuid.UUID{uuid.New()},
					}
					plan["b"] = AppInboxSlot{
						AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{},
						ArtifactIDs: []uuid.UUID{unused},
					}
				}
				var err error
				receipt.Plan, err = json.Marshal(plan)
				require.NoError(t, err)
			}
			store := &appPlanIntegrations{receipt: receipt, appSetup: integrationstore.ProjectAppRecord{
				ID: app, ProjectID: project, Provider: "slack",
			}}
			provider := &failedInboxProvider{err: errors.New("provider unavailable")}
			artifacts := &failedInboxArtifacts{}
			consumer := NewAppInboxConsumer(nil, store, artifacts, map[string]AppInboxProvider{"slack": provider}, nil, nil)
			err := consumer.FinalizeFailure(t.Context(), project, receiptID)
			if scenario.wantNotice {
				require.ErrorIs(t, err, provider.err)
				require.Equal(t, 1, provider.notices)
				if scenario.name == "partially admitted" {
					require.Equal(t, "I couldn't deliver this request to every agent. Some agents have already received it.", provider.message)
				} else {
					require.Equal(t, inboxFailureMessage, provider.message)
				}
			} else {
				require.NoError(t, err)
				require.Zero(t, provider.notices)
			}
			if scenario.wantCleanup {
				require.Equal(t, []uuid.UUID{unused}, artifacts.deleted, "failed notification must not skip unused-file cleanup")
			} else {
				require.Empty(t, artifacts.deleted)
			}
			require.Equal(t, receipt, store.receipt)
			store.receipt.State = integrationstore.IntegrationInboxPending
			require.ErrorIs(t, consumer.FinalizeFailure(t.Context(), project, receiptID), storeerr.ErrStateTransitionConflict)
		})
	}
}
