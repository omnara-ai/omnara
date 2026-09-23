package apps

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
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
	_ context.Context, _ appstore.ProjectAppRecord, _ appstore.AppInboxRecord, message string,
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
		wantPartial    bool
		wantCleanup    []int
	}{
		{"before planning", `{}`, false, false, true, false, nil},
		{"unrouted", `{}`, true, true, false, false, nil},
		{"complete bookkeeping failed", `{"a":{"committed":{}},"b":{"committed":{}}}`, true, false, false, false, nil},
		{"partially admitted", `{"a":{"committed":{}}}`, true, false, true, true, []int{1}},
		{"skipped and unfinished", `{"a":{"committed":{"skipped":"agent_archived"}}}`, true, false, true, false, []int{0, 1}},
		{
			"skipped and delivered", `{"a":{"committed":{"skipped":"agent_archived"}},"b":{"committed":{}}}`,
			true, false, false, false, []int{0},
		},
		{
			"all skipped", `{"a":{"committed":{"skipped":"agent_archived"}},"b":{"committed":{"skipped":"agent_archived"}}}`,
			true, false, false, false, []int{0, 1},
		},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			project, app, receiptID := uuid.New(), uuid.New(), uuid.New()
			files := []uuid.UUID{uuid.New(), uuid.New()}
			receipt := appstore.AppInboxRecord{
				ID: receiptID, ProjectID: project, AppID: app,
				State: appstore.AppInboxFailed, Progress: json.RawMessage(scenario.progress),
			}
			if scenario.planned {
				plan := AppInboxPlan{}
				if !scenario.empty {
					plan["a"] = AppInboxSlot{
						AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{},
						ArtifactIDs: []uuid.UUID{files[0]},
					}
					plan["b"] = AppInboxSlot{
						AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{},
						ArtifactIDs: []uuid.UUID{files[1]},
					}
				}
				var err error
				receipt.Plan, err = json.Marshal(plan)
				require.NoError(t, err)
			}
			store := &appPlanStore{receipt: receipt, appSetup: appstore.ProjectAppRecord{
				ID: app, ProjectID: project, Provider: "slack",
			}}
			provider := &failedInboxProvider{err: errors.New("provider unavailable")}
			artifacts := &failedInboxArtifacts{}
			consumer := NewAppInboxConsumer(nil, store, artifacts, map[string]AppInboxProvider{"slack": provider}, nil, nil)
			err := consumer.FinalizeFailure(t.Context(), project, receiptID)
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
			var wantDeleted []uuid.UUID
			for _, i := range scenario.wantCleanup {
				wantDeleted = append(wantDeleted, files[i])
			}
			require.Equal(t, wantDeleted, artifacts.deleted, "notice outcome must not skip unused-file cleanup")
			require.Equal(t, receipt, store.receipt)
			store.receipt.State = appstore.AppInboxPending
			require.ErrorIs(t, consumer.FinalizeFailure(t.Context(), project, receiptID), storeerr.ErrStateTransitionConflict)
		})
	}
}

func TestAppFailureCleanupReadsOnlyForPlannedArtifacts(t *testing.T) {
	for _, scenario := range []string{"text only", "file"} {
		t.Run(scenario, func(t *testing.T) {
			projectID, receiptID, appID := uuid.New(), uuid.New(), uuid.New()
			slot := AppInboxSlot{AgentID: uuid.New(), Input: &executionstore.CreateAgentContentInputInput{}}
			if scenario == "file" {
				slot.ArtifactIDs = []uuid.UUID{uuid.New()}
			}
			plan, err := json.Marshal(AppInboxPlan{"slot": slot})
			require.NoError(t, err)
			inbox := &cleanupCountingInbox{appPlanStore: appPlanStore{
				receipt: appstore.AppInboxRecord{
					ID: receiptID, ProjectID: projectID, AppID: appID,
					State: appstore.AppInboxFailed, Plan: plan, Progress: json.RawMessage(`{}`),
				},
				appSetup: appstore.ProjectAppRecord{ID: appID, ProjectID: projectID, Provider: "slack"},
			}}
			provider := &failedInboxProvider{}
			artifacts := &failedInboxArtifacts{}
			consumer := NewAppInboxConsumer(nil, inbox, artifacts, map[string]AppInboxProvider{"slack": provider}, nil, nil)
			require.NoError(t, consumer.FinalizeFailure(t.Context(), projectID, receiptID))
			require.Equal(t, 1, provider.notices)
			require.Equal(t, inboxFailureMessage, provider.message)
			if scenario == "file" {
				require.Equal(t, 2, inbox.reads)
				require.Equal(t, slot.ArtifactIDs, artifacts.deleted)
			} else {
				require.Equal(t, 1, inbox.reads, "text-only failure needs no cleanup receipt read")
				require.Empty(t, artifacts.deleted)
			}
		})
	}
}
