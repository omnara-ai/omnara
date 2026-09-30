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
	integrationConsumerProvider
	notices int
	message string
	err     error
}

func (p *failedInboxProvider) NotifyInboxFailure(
	_ context.Context,
	_ integrationstore.IntegrationRecord,
	_ integrationstore.IntegrationInboxRecord,
	message string,
) error {
	p.notices++
	p.message = message
	return p.err
}

type failedInboxArtifacts struct {
	integrationConsumerUploads
	checked []uuid.UUID
}

func (a *failedInboxArtifacts) DeleteUnreferencedPreparedArtifact(
	_ context.Context, _, _ uuid.UUID, artifact uuid.UUID,
) error {
	a.checked = append(a.checked, artifact)
	return nil
}

type failedInboxExecution struct {
	IntegrationExecutionStore
	outcomes map[string]executionstore.InboxRecipientOutcome
	err      error
}

func (s failedInboxExecution) GetIntegrationInboxOutcomes(
	context.Context, integrationstore.IntegrationInboxRecord,
) (map[string]executionstore.InboxRecipientOutcome, error) {
	return s.outcomes, s.err
}

func TestIntegrationFailureFinalizationPreservesAcceptedWork(t *testing.T) {
	for _, scenario := range []struct {
		name           string
		outcomes       map[string]executionstore.InboxRecipientOutcome
		planned, empty bool
		wantNotice     bool
		wantPartial    bool
	}{
		{"before planning", nil, false, false, true, false},
		{"unrouted", nil, true, true, false, false},
		{
			"complete bookkeeping failed",
			map[string]executionstore.InboxRecipientOutcome{
				"a": executionstore.InboxRecipientDelivered, "b": executionstore.InboxRecipientDelivered,
			},
			true, false, false, false,
		},
		{
			"partially admitted",
			map[string]executionstore.InboxRecipientOutcome{
				"a": executionstore.InboxRecipientDelivered, "b": executionstore.InboxRecipientPending,
			},
			true, false, true, true,
		},
		{
			"skipped and unfinished",
			map[string]executionstore.InboxRecipientOutcome{
				"a": executionstore.InboxRecipientSkipped, "b": executionstore.InboxRecipientPending,
			},
			true, false, true, false,
		},
		{
			"skipped and delivered",
			map[string]executionstore.InboxRecipientOutcome{
				"a": executionstore.InboxRecipientSkipped, "b": executionstore.InboxRecipientDelivered,
			},
			true, false, false, false,
		},
		{
			"all skipped",
			map[string]executionstore.InboxRecipientOutcome{
				"a": executionstore.InboxRecipientSkipped, "b": executionstore.InboxRecipientSkipped,
			},
			true, false, false, false,
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, integration, receiptID := uuid.New(), uuid.New(), uuid.New()
			files := []uuid.UUID{uuid.New(), uuid.New()}
			receipt := integrationstore.IntegrationInboxRecord{
				ID: receiptID, ProjectID: project, IntegrationID: integration,
				State: integrationstore.IntegrationInboxFailed,
			}
			if scenario.planned {
				plan := IntegrationInboxPlan{
					Message: &executionstore.InboxMessage{}, Recipients: map[string]IntegrationInboxRecipient{},
				}
				if !scenario.empty {
					plan.Recipients["a"] = IntegrationInboxRecipient{
						AgentID:     uuid.New(),
						ArtifactIDs: []uuid.UUID{files[0]},
					}
					plan.Recipients["b"] = IntegrationInboxRecipient{
						AgentID:     uuid.New(),
						ArtifactIDs: []uuid.UUID{files[1]},
					}
				}
				var err error
				receipt.Plan, err = json.Marshal(plan)
				require.NoError(t, err)
			}
			store := &integrationPlanStore{receipt: receipt, integrationSetup: integrationstore.IntegrationRecord{
				ID: integration, ProjectID: project, Provider: "slack",
			}}
			provider := &failedInboxProvider{err: errors.New("provider unavailable")}
			artifacts := &failedInboxArtifacts{}
			consumer := NewIntegrationInboxConsumer(
				NewIntegrationRouter(failedInboxExecution{outcomes: scenario.outcomes}, store),
				store,
				artifacts,
				map[string]IntegrationInboxProvider{"slack": provider},
				nil,
				nil,
			)
			err := consumer.FinalizeFailure(t.Context(), project, receiptID, nil)
			if scenario.wantNotice {
				require.ErrorIs(t, err, provider.err)
				require.Equal(t, 1, provider.notices)
				if scenario.wantPartial {
					require.Equal(t,
						"I couldn't deliver this request to every agent. Some agents have already received it.", provider.message)
				} else {
					require.Equal(t, inboxFailureMessage, provider.message)
				}
			} else {
				require.NoError(t, err)
				require.Zero(t, provider.notices)
			}
			var wantChecked []uuid.UUID
			if scenario.planned && !scenario.empty {
				wantChecked = files
			}
			require.Equal(t, wantChecked, artifacts.checked, "notice outcome must not skip durable-reference checks")
			require.Equal(t, receipt, store.receipt)
			store.receipt.State = integrationstore.IntegrationInboxQueued
			require.ErrorIs(t, consumer.FinalizeFailure(t.Context(), project, receiptID, nil),
				storeerr.ErrStateTransitionConflict)
		})
	}
}

func TestIntegrationFailureCleanupReadsOnlyForPlannedArtifacts(t *testing.T) {
	for _, scenario := range []string{"text only", "file", "outcome unavailable"} {
		t.Run(scenario, func(t *testing.T) {
			projectID, receiptID, integrationID := uuid.New(), uuid.New(), uuid.New()
			recipient := IntegrationInboxRecipient{AgentID: uuid.New()}
			if scenario != "text only" {
				recipient.ArtifactIDs = []uuid.UUID{uuid.New()}
			}
			plan, err := json.Marshal(IntegrationInboxPlan{
				Message: &executionstore.InboxMessage{}, Recipients: map[string]IntegrationInboxRecipient{"recipient": recipient},
			})
			require.NoError(t, err)
			inbox := &cleanupCountingInbox{integrationPlanStore: integrationPlanStore{
				receipt: integrationstore.IntegrationInboxRecord{
					ID: receiptID, ProjectID: projectID, IntegrationID: integrationID,
					State: integrationstore.IntegrationInboxFailed, Plan: plan,
				},
				integrationSetup: integrationstore.IntegrationRecord{
					ID:        integrationID,
					ProjectID: projectID,
					Provider:  "slack",
				},
			}}
			provider := &failedInboxProvider{}
			artifacts := &failedInboxArtifacts{}
			var outcomeErr error
			if scenario == "outcome unavailable" {
				outcomeErr = errors.New("database temporarily unavailable")
			}
			consumer := NewIntegrationInboxConsumer(
				NewIntegrationRouter(failedInboxExecution{
					err:      outcomeErr,
					outcomes: map[string]executionstore.InboxRecipientOutcome{"recipient": executionstore.InboxRecipientPending},
				}, inbox),
				inbox,
				artifacts,
				map[string]IntegrationInboxProvider{"slack": provider},
				nil,
				nil,
			)
			err = consumer.FinalizeFailure(t.Context(), projectID, receiptID, nil)
			if outcomeErr != nil {
				require.ErrorIs(t, err, outcomeErr)
				require.Zero(t, provider.notices, "unknown delivery must not produce a misleading failure notice")
			} else {
				require.NoError(t, err)
				require.Equal(t, 1, provider.notices)
				require.Equal(t, inboxFailureMessage, provider.message)
			}
			if scenario != "text only" {
				require.Equal(t, 2, inbox.reads)
				require.Equal(t, recipient.ArtifactIDs, artifacts.checked)
			} else {
				require.Equal(t, 1, inbox.reads, "text-only failure needs no cleanup receipt read")
				require.Empty(t, artifacts.checked)
			}
		})
	}
}

func TestIntegrationFailureAdmissionDeniedContactsOwner(t *testing.T) {
	project, integration, receiptID := uuid.New(), uuid.New(), uuid.New()
	store := &integrationPlanStore{receipt: integrationstore.IntegrationInboxRecord{
		ID: receiptID, ProjectID: project, IntegrationID: integration, State: integrationstore.IntegrationInboxFailed,
	}, integrationSetup: integrationstore.IntegrationRecord{ID: integration, ProjectID: project, Provider: "slack"}}
	provider := &failedInboxProvider{}
	consumer := NewIntegrationInboxConsumer(
		nil, store, nil, map[string]IntegrationInboxProvider{"slack": provider}, nil, nil,
	)
	require.NoError(t, consumer.FinalizeFailure(t.Context(), project, receiptID, storeerr.ErrManagedWorkAdmissionDenied))
	require.Equal(t, 1, provider.notices)
	require.Equal(t, launchUnavailableMessage, provider.message)
}

func TestIntegrationFailureUnauthorizedDoesNotImplyUnavailableProfile(t *testing.T) {
	t.Parallel()
	project, integration, receiptID := uuid.New(), uuid.New(), uuid.New()
	store := &integrationPlanStore{receipt: integrationstore.IntegrationInboxRecord{
		ID: receiptID, ProjectID: project, IntegrationID: integration, State: integrationstore.IntegrationInboxFailed,
	}, integrationSetup: integrationstore.IntegrationRecord{ID: integration, ProjectID: project, Provider: "slack"}}
	provider := &failedInboxProvider{}
	consumer := NewIntegrationInboxConsumer(
		nil, store, nil, map[string]IntegrationInboxProvider{"slack": provider}, nil, nil,
	)
	require.NoError(t, consumer.FinalizeFailure(t.Context(), project, receiptID, storeerr.ErrUnauthorized))
	require.Equal(t, 1, provider.notices)
	require.Equal(t, inboxFailureMessage, provider.message)
}
