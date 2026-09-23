package apps

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

type AppEvent struct {
	Launches               []AppLaunchIntent                     `json:"launches,omitempty"`
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

type AppInboxSlot struct {
	Sibling      *executionstore.InboxMessageSibling          `json:"sibling,omitempty"`
	Scope        appdefinition.Scope                          `json:"scope"`
	EventOrder   int                                          `json:"event_order"`
	Selection    *appstore.InboxAppSelection                  `json:"selection,omitempty"`
	AgentID      uuid.UUID                                    `json:"agent_id"`
	Launch       *executionstore.InboxLaunchPlan              `json:"launch,omitempty"`
	Input        *executionstore.CreateAgentContentInputInput `json:"input,omitempty"`
	ArtifactIDs  []uuid.UUID                                  `json:"artifact_ids,omitempty"`
	Files        []AppPlannedFile                             `json:"files,omitempty"`
	Subscription *executionstore.InboxSubscriptionAuthority   `json:"subscription,omitempty"`
}

type AppInboxPlan map[string]AppInboxSlot

type AppExecutionStore interface {
	CreateAgentConfig(context.Context, executionstore.CreateAgentConfigInput) (executionstore.AgentConfigRecord, error)
	GetAgentInProject(context.Context, uuid.UUID, uuid.UUID) (executionstore.AgentRecord, error)
	CheckInboxConversationAuthority(
		context.Context,
		appstore.AppInboxLease,
		string,
		appstore.ConversationAddress,
	) error
	GetAgentProfile(context.Context, uuid.UUID, uuid.UUID) (executionstore.AgentProfileRecord, error)
	AdmitInboxLaunchSlot(
		context.Context,
		appstore.AppInboxLease,
		string,
	) (executionstore.LaunchAgentResult, error)
	AdmitInboxInputSlot(
		context.Context,
		appstore.AppInboxLease,
		string,
	) (executionstore.InboxInputResult, error)
}

type AppRoutingStore interface {
	GetAppInbox(context.Context, uuid.UUID, uuid.UUID) (appstore.AppInboxRecord, error)
	GetProjectAppByID(context.Context, uuid.UUID) (appstore.ProjectAppRecord, error)
	WithAppInboxLease(
		context.Context,
		appstore.AppInboxLease,
		func(*appstore.AppInboxLeaseTx) error,
	) error
	AppRoutingCandidatesForInbox(
		context.Context,
		*appstore.AppInboxLeaseTx,
		appstore.ConversationAddress,
		[]appstore.ConversationAddress,
		string,
	) (appstore.AppRoutingCandidates, error)
}

type AppRouter struct {
	execution AppExecutionStore
	apps      AppRoutingStore
}

func NewAppRouter(execution AppExecutionStore, apps AppRoutingStore) *AppRouter {
	return &AppRouter{execution: execution, apps: apps}
}

func (r *AppRouter) Prepare(
	ctx context.Context,
	lease appstore.AppInboxLease,
	slot string,
	artifacts []artifactstore.PreparedArtifact,
) error {
	value, err := json.Marshal(executionstore.InboxInputPreparation{Artifacts: artifacts})
	if err != nil {
		return err
	}
	return r.apps.WithAppInboxLease(
		ctx,
		lease,
		func(work *appstore.AppInboxLeaseTx) error {
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

func (r *AppRouter) Admit(
	ctx context.Context,
	lease appstore.AppInboxLease,
) ([]AppSlotAdmission, error) {
	receipt, err := r.apps.GetAppInbox(ctx, lease.ProjectID, lease.ReceiptID)
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
	if receipt.State == appstore.AppInboxCompleted {
		return results, nil
	}
	err = r.apps.WithAppInboxLease(
		ctx,
		lease,
		func(work *appstore.AppInboxLeaseTx) error { return work.Complete(ctx) },
	)
	if err != nil {
		if latest, readErr := r.apps.GetAppInbox(
			ctx,
			lease.ProjectID,
			lease.ReceiptID,
		); readErr == nil &&
			latest.State == appstore.AppInboxCompleted {
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
