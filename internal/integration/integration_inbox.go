package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type IntegrationEvent struct {
	Launches               []IntegrationLaunchIntent             `json:"launches,omitempty"`
	Directed               bool                                  `json:"directed,omitempty"`
	Sibling                *executionstore.InboxMessageSibling   `json:"sibling,omitempty"`
	Event                  integrationdefinition.Event           `json:"event"`
	SemanticKey            string                                `json:"semantic_key"`
	DisplayName            string                                `json:"display_name,omitempty"`
	ContentBlocks          json.RawMessage                       `json:"content_blocks"`
	Metadata               json.RawMessage                       `json:"metadata,omitempty"`
	Actor                  executionstore.ActorParams            `json:"actor"`
	DeliveryMode           executionstore.AgentInputDeliveryMode `json:"delivery_mode,omitempty"`
	CancelOpenInteractions bool                                  `json:"cancel_open_interactions,omitempty"`
	Files                  []IntegrationPlannedFile              `json:"files,omitempty"`
}

type IntegrationLaunchIntent struct {
	IntegrationID uuid.UUID `json:"integration_id"`
	Slot          string    `json:"slot"`
	ProfileID     uuid.UUID `json:"profile_id,omitempty"`
	AgentID       uuid.UUID `json:"agent_id,omitempty"`
}

type IntegrationPlannedFile struct {
	ArtifactID     uuid.UUID                       `json:"artifact_id"`
	ProviderFileID string                          `json:"provider_file_id"`
	Expected       *artifactstore.PreparedArtifact `json:"expected,omitempty"`
}

type IntegrationInboxSlot struct {
	Sibling      *executionstore.InboxMessageSibling          `json:"sibling,omitempty"`
	Scope        integrationdefinition.Scope                  `json:"scope"`
	EventOrder   int                                          `json:"event_order"`
	Selection    *integrationstore.InboxIntegrationSelection  `json:"selection,omitempty"`
	AgentID      uuid.UUID                                    `json:"agent_id"`
	Launch       *executionstore.InboxLaunchPlan              `json:"launch,omitempty"`
	Input        *executionstore.CreateAgentContentInputInput `json:"input,omitempty"`
	ArtifactIDs  []uuid.UUID                                  `json:"artifact_ids,omitempty"`
	Files        []IntegrationPlannedFile                     `json:"files,omitempty"`
	Subscription *executionstore.InboxSubscriptionAuthority   `json:"subscription,omitempty"`
}

type IntegrationInboxPlan map[string]IntegrationInboxSlot

type IntegrationExecutionStore interface {
	CreateAgentConfig(context.Context, executionstore.CreateAgentConfigInput) (executionstore.AgentConfigRecord, error)
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

type IntegrationRoutingStore interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
	GetProjectIntegrationByID(context.Context, uuid.UUID) (integrationstore.ProjectIntegrationRecord, error)
	WithIntegrationInboxLease(
		context.Context,
		integrationstore.IntegrationInboxLease,
		func(*integrationstore.IntegrationInboxLeaseTx) error,
	) error
	IntegrationRoutingCandidatesForInbox(
		context.Context,
		*integrationstore.IntegrationInboxLeaseTx,
		integrationstore.ConversationAddress,
		[]integrationstore.ConversationAddress,
		string,
	) (integrationstore.IntegrationRoutingCandidates, error)
}

type IntegrationRouter struct {
	execution    IntegrationExecutionStore
	integrations IntegrationRoutingStore
}

func NewIntegrationRouter(
	execution IntegrationExecutionStore,
	integrations IntegrationRoutingStore,
) *IntegrationRouter {
	return &IntegrationRouter{execution: execution, integrations: integrations}
}

func (r *IntegrationRouter) Prepare(
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
			plan, err := decodeIntegrationInboxPlan(work.Receipt().Plan)
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

type IntegrationSlotAdmission struct {
	Slot   string
	Launch *executionstore.LaunchAgentResult
	Input  *executionstore.InboxInputResult
}

func (r *IntegrationRouter) Admit(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
) ([]IntegrationSlotAdmission, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	plan, err := decodeIntegrationInboxPlan(receipt.Plan)
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
	results := make([]IntegrationSlotAdmission, 0, len(keys))
	var failures []error
	for _, key := range keys {
		result := IntegrationSlotAdmission{Slot: key}
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

func decodeIntegrationInboxPlan(raw json.RawMessage) (IntegrationInboxPlan, error) {
	var plan IntegrationInboxPlan
	if json.Unmarshal(raw, &plan) != nil || plan == nil {
		return nil, fmt.Errorf("receipt has no valid frozen integration plan")
	}
	for key, slot := range plan {
		if slot.AgentID == uuid.Nil || (slot.Launch == nil) == (slot.Input == nil) ||
			(slot.Selection != nil) != (slot.Launch != nil) {
			return nil, fmt.Errorf("invalid integration plan slot %s", key)
		}
	}
	return plan, nil
}
