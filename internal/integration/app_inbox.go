package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

// AppEvent is normalized only after provider verification/expansion. Content
// uses ordinary input blocks. Each media_ref has a placeholder artifact UUID
// mapped to an immutable provider file ID; planning assigns fresh per-agent IDs.
// Downloads and uploads happen after Freeze and outside every store transaction.
type AppEvent struct {
	// Launches are explicit decisions made by app code before generic planning.
	Launches []AppLaunchIntent `json:"launches,omitempty"`
	// Directed requests deliver only to their named setup/slot recipients. In
	// particular, choosing an old menu must not replay its source to new subscriptions.
	Directed               bool                                  `json:"directed,omitempty"`
	Sibling                *executionstore.InboxMessageSibling   `json:"sibling,omitempty"`
	Event                  appdefinition.Event                   `json:"event"`
	SemanticKey            string                                `json:"semantic_key"`
	DisplayName            string                                `json:"display_name,omitempty"`
	ContentBlocks          json.RawMessage                       `json:"content_blocks"`
	Metadata               json.RawMessage                       `json:"metadata,omitempty"`
	Actor                  executionstore.ActorParams            `json:"actor"`
	DeliveryMode           executionstore.AgentInputDeliveryMode `json:"delivery_mode,omitempty"`
	CancelOpenInteractions bool                                  `json:"cancel_open_interactions,omitempty"`
	Files                  []AppPlannedFile                      `json:"files,omitempty"`
}

type AppLaunchIntent struct {
	AppID     uuid.UUID `json:"app_id"`
	Slot      string    `json:"slot"`
	ProfileID uuid.UUID `json:"profile_id,omitempty"`
	AgentID   uuid.UUID `json:"agent_id,omitempty"`
}

type AppPlannedFile struct {
	ArtifactID     uuid.UUID                       `json:"artifact_id"`
	ProviderFileID string                          `json:"provider_file_id"`
	Expected       *artifactstore.PreparedArtifact `json:"expected,omitempty"`
}

// AppInboxSlot deliberately shares the kernel admission JSON envelopes. Only
// profile launches contain Selection; existing agents never reserve membership.
// BaseConfig identifies what was read. Launch freezes the derived tool/handler
// config and concrete subscriptions, including resolved events. Recovery admits
// those snapshots without rebuilding either from current source.
type AppInboxSlot struct {
	Sibling      *executionstore.InboxMessageSibling          `json:"sibling,omitempty"`
	Scope        appdefinition.Scope                          `json:"scope"`
	EventOrder   int                                          `json:"event_order"`
	Selection    *integrationstore.InboxAppSelection          `json:"selection,omitempty"`
	AgentID      uuid.UUID                                    `json:"agent_id"`
	Launch       *executionstore.LaunchAgentInput             `json:"launch,omitempty"`
	Input        *executionstore.CreateAgentContentInputInput `json:"input,omitempty"`
	ArtifactIDs  []uuid.UUID                                  `json:"artifact_ids,omitempty"`
	Files        []AppPlannedFile                             `json:"files,omitempty"`
	BaseConfigID uuid.UUID                                    `json:"base_config_id,omitempty"`
	Subscription *executionstore.InboxSubscriptionAuthority   `json:"subscription,omitempty"`
}

type AppInboxPlan map[string]AppInboxSlot

type AppExecutionStore interface {
	GetAgentConfig(context.Context, uuid.UUID, uuid.UUID) (executionstore.AgentConfigRecord, bool, error)
	GetAgentInProject(context.Context, uuid.UUID, uuid.UUID) (executionstore.AgentRecord, error)
	CheckInboxConversationAuthority(
		context.Context,
		integrationstore.IntegrationInboxLease,
		string,
		integrationstore.ConversationAddress,
	) error
	GetAgentProfile(context.Context, uuid.UUID, uuid.UUID) (executionstore.AgentProfileRecord, error)
	AdmitInboxLaunchSlot(
		context.Context,
		integrationstore.IntegrationInboxLease,
		string,
	) (executionstore.LaunchAgentResult, error)
	AdmitInboxInputSlot(
		context.Context,
		integrationstore.IntegrationInboxLease,
		string,
	) (executionstore.InboxInputResult, error)
}

type AppRoutingStore interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
	GetProjectAppByID(context.Context, uuid.UUID) (integrationstore.ProjectAppRecord, error)
	WithIntegrationInboxLease(
		context.Context,
		integrationstore.IntegrationInboxLease,
		func(*integrationstore.IntegrationInboxLeaseTx) error,
	) error
	AppRoutingCandidatesForInbox(
		context.Context,
		*integrationstore.IntegrationInboxLeaseTx,
		integrationstore.ConversationAddress,
		[]integrationstore.ConversationAddress,
		string,
	) (integrationstore.AppRoutingCandidates, error)
}

// AppRouter owns bounded planning and semantic admission. Provider intake,
// downloads, worker scheduling and retry budgets remain with their existing
// owners. Errors retain the receipt/plan for explicit retry or diagnosis.
type AppRouter struct {
	execution    AppExecutionStore
	integrations AppRoutingStore
}

func NewAppRouter(execution AppExecutionStore, integrations AppRoutingStore) *AppRouter {
	return &AppRouter{execution: execution, integrations: integrations}
}

// Prepare records metadata for bytes already uploaded at the frozen identities.
func (r *AppRouter) Prepare(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	slot string,
	artifacts []artifactstore.PreparedArtifact,
) error {
	value, err := json.Marshal(executionstore.InboxInputPreparation{Artifacts: artifacts})
	if err != nil {
		return err
	}
	return r.integrations.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error {
			plan, err := decodeAppInboxPlan(work.Receipt().Plan)
			if err != nil {
				return err
			}
			planned, exists := plan[slot]
			if !exists || len(planned.ArtifactIDs) != len(artifacts) {
				return fmt.Errorf("preparation differs from frozen files")
			}
			ids := make(map[uuid.UUID]bool, len(planned.ArtifactIDs))
			for _, id := range planned.ArtifactIDs {
				ids[id] = true
			}
			for _, artifact := range artifacts {
				if !ids[artifact.ID] {
					return fmt.Errorf("preparation contains an unplanned or duplicate artifact")
				}
				delete(ids, artifact.ID)
				if err := artifact.Validate(); err != nil {
					return err
				}
				for _, file := range planned.Files {
					if file.ArtifactID == artifact.ID && file.Expected != nil && *file.Expected != artifact {
						return fmt.Errorf("prepared artifact differs from frozen content")
					}
				}
			}
			return work.PrepareSlot(ctx, slot, value)
		},
	)
}

type AppSlotAdmission struct {
	Slot   string
	Launch *executionstore.LaunchAgentResult
	Input  *executionstore.InboxInputResult
}

// Admit attempts each frozen recipient independently, preserving successful
// progress when another slot fails. Empty plans complete only after a successful
// Freeze; a reservation conflict never becomes a silently completed empty plan.
func (r *AppRouter) Admit(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
) ([]AppSlotAdmission, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	plan, err := decodeAppInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(plan))
	for key := range plan {
		keys = append(keys, key)
	}
	slices.SortFunc(keys, func(a, b string) int {
		if plan[a].EventOrder < plan[b].EventOrder {
			return -1
		}
		if plan[a].EventOrder > plan[b].EventOrder {
			return 1
		}
		return slices.Compare([]byte(a), []byte(b))
	})
	results := make([]AppSlotAdmission, 0, len(keys))
	var failures []error
	for _, key := range keys {
		result := AppSlotAdmission{Slot: key}
		if plan[key].Launch != nil {
			value, admitErr := r.execution.AdmitInboxLaunchSlot(ctx, lease, key)
			err = admitErr
			result.Launch = &value
		} else {
			value, admitErr := r.execution.AdmitInboxInputSlot(ctx, lease, key)
			err = admitErr
			result.Input = &value
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("slot %s: %w", key, err))
			continue
		}
		results = append(results, result)
	}
	if err := errors.Join(failures...); err != nil {
		return results, err
	}
	if receipt.State == integrationstore.IntegrationInboxCompleted {
		return results, nil
	}
	err = r.integrations.WithIntegrationInboxLease(
		ctx,
		lease,
		func(work *integrationstore.IntegrationInboxLeaseTx) error { return work.Complete(ctx) },
	)
	if err != nil {
		if latest, readErr := r.integrations.GetIntegrationInbox(
			ctx,
			lease.ProjectID,
			lease.ReceiptID,
		); readErr == nil &&
			latest.State == integrationstore.IntegrationInboxCompleted {
			return results, nil
		}
	}
	return results, err
}

func decodeAppInboxPlan(raw json.RawMessage) (AppInboxPlan, error) {
	var plan AppInboxPlan
	if json.Unmarshal(raw, &plan) != nil || plan == nil {
		return nil, fmt.Errorf("receipt has no valid frozen app plan")
	}
	for key, slot := range plan {
		if slot.AgentID == uuid.Nil || (slot.Launch == nil) == (slot.Input == nil) ||
			(slot.Selection != nil) != (slot.Launch != nil) {
			return nil, fmt.Errorf("invalid app plan slot %s", key)
		}
	}
	return plan, nil
}
