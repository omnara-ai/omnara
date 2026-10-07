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
	Files                  []executionstore.InboxPlannedFile     `json:"files,omitempty"`
}

type IntegrationLaunchIntent struct {
	IntegrationID uuid.UUID `json:"integration_id"`
	LaunchKey     string    `json:"launch_key"`
	ProfileID     uuid.UUID `json:"profile_id,omitempty"`
}

type IntegrationInboxRecipient struct {
	LaunchClaim  *integrationstore.InboxLaunchClaim         `json:"launch_claim,omitempty"`
	AgentID      uuid.UUID                                  `json:"agent_id"`
	Launch       *executionstore.InboxLaunchPlan            `json:"launch,omitempty"`
	ArtifactIDs  []uuid.UUID                                `json:"artifact_ids,omitempty"`
	Subscription *executionstore.InboxSubscriptionAuthority `json:"subscription,omitempty"`
}

type IntegrationInboxPlan struct {
	Message    *executionstore.InboxMessage         `json:"message,omitempty"`
	Recipients map[string]IntegrationInboxRecipient `json:"recipients"`
}

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
	GetIntegrationInboxOutcomes(
		context.Context, integrationstore.IntegrationInboxRecord,
	) (map[string]executionstore.InboxRecipientOutcome, error)
	CompleteIntegrationInbox(context.Context, integrationstore.IntegrationInboxLease) error
	AdmitInboxLaunchRecipient(
		context.Context,
		integrationstore.IntegrationInboxLease,
		string,
		[]artifactstore.PreparedArtifact,
	) (executionstore.LaunchAgentResult, error)
	AdmitInboxInputRecipient(
		context.Context,
		integrationstore.IntegrationInboxLease,
		string,
		[]artifactstore.PreparedArtifact,
	) (executionstore.InboxInputResult, error)
}

type IntegrationRoutingStore interface {
	GetIntegrationInbox(context.Context, uuid.UUID, uuid.UUID) (integrationstore.IntegrationInboxRecord, error)
	GetIntegrationByID(context.Context, uuid.UUID) (integrationstore.IntegrationRecord, error)
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

type IntegrationRecipientAdmission struct {
	Recipient string
	Launch    *executionstore.LaunchAgentResult
	Input     *executionstore.InboxInputResult
}

func (r *IntegrationRouter) Admit(
	ctx context.Context,
	lease integrationstore.IntegrationInboxLease,
	prepared map[string][]artifactstore.PreparedArtifact,
) ([]IntegrationRecipientAdmission, error) {
	receipt, err := r.integrations.GetIntegrationInbox(ctx, lease.ProjectID, lease.ReceiptID)
	if err != nil {
		return nil, err
	}
	plan, err := decodeIntegrationInboxPlan(receipt.Plan)
	if err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(plan.Recipients))
	for key := range plan.Recipients {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	results := make([]IntegrationRecipientAdmission, 0, len(keys))
	var failures []error
	for _, key := range keys {
		result := IntegrationRecipientAdmission{Recipient: key}
		if plan.Recipients[key].Launch != nil {
			value, admitErr := r.execution.AdmitInboxLaunchRecipient(ctx, lease, key, prepared[key])
			err = admitErr
			result.Launch = &value
		} else {
			value, admitErr := r.execution.AdmitInboxInputRecipient(ctx, lease, key, prepared[key])
			err = admitErr
			result.Input = &value
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("recipient %s: %w", key, err))
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
	err = r.execution.CompleteIntegrationInbox(ctx, lease)
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
	if json.Unmarshal(raw, &plan) != nil || plan.Recipients == nil {
		return IntegrationInboxPlan{}, fmt.Errorf("receipt has no valid frozen integration plan")
	}
	for key, recipient := range plan.Recipients {
		if plan.Message == nil || recipient.AgentID == uuid.Nil ||
			(recipient.LaunchClaim != nil) != (recipient.Launch != nil) ||
			(recipient.Launch != nil && recipient.Subscription != nil) {
			return IntegrationInboxPlan{}, fmt.Errorf("invalid integration plan recipient %s", key)
		}
	}
	return plan, nil
}
