package executionstore

import (
	"encoding/json"

	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type CreateIntegrationTargetContentInput struct {
	IntegrationInstallID       ID
	IntegrationTargetID        ID
	IntegrationTargetBindingID ID
	AgentID                    ID
	RefreshTarget              bool
	TargetDisplayName          string
	TargetProviderMetadata     json.RawMessage
	ProviderTenantID           string
	ProviderUserID             string
	ActorDisplayName           string
	ContentBlocks              json.RawMessage
	Metadata                   json.RawMessage
	DeliveryMode               AgentInputDeliveryMode
	IdempotencyKey             string
	CancelOpenInteractions     bool
	RuntimeLease               *IntegrationRuntimeLeaseProof
}

// CreateBoundIntegrationTargetContentInput keeps provider-address resolution,
// append-only authorization replacement, runtime fencing, and input creation in
// one transaction. Connector ingress uses this operation so a stale runtime
// cannot mutate channel authorization without also creating its fenced input.
type CreateBoundIntegrationTargetContentInput struct {
	Target                 integrationstore.CreateIntegrationTargetInput
	IntegrationRouteID     ID
	ReceiveAllowed         bool
	SendAllowed            bool
	BindingSource          string
	BindingMetadata        json.RawMessage
	ProviderTenantID       string
	ProviderUserID         string
	ActorDisplayName       string
	ContentBlocks          json.RawMessage
	Metadata               json.RawMessage
	DeliveryMode           AgentInputDeliveryMode
	IdempotencyKey         string
	CancelOpenInteractions bool
	RuntimeLease           *IntegrationRuntimeLeaseProof
}

type CreateBoundIntegrationTargetContentResult struct {
	AgentInput                 AgentInputRecord
	CanceledInteractionIDs     []ID
	IntegrationTargetID        ID
	IntegrationTargetBindingID ID
}

// IntegrationRuntimeLeaseProof fences mutations originating from a leased
// persistent connector. Webhook and native ingress leave it nil.
type IntegrationRuntimeLeaseProof = integrationstore.IntegrationRuntimeLeaseProof

type GetIntegrationTargetInputByIdempotencyInput struct {
	IntegrationInstallID ID
	IntegrationTargetID  ID
	IdempotencyKey       string
}
