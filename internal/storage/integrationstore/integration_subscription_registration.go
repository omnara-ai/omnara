package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func PrepareIntegrationSubscriptionTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID uuid.UUID,
	attachment IntegrationSubscriptionAttachment,
) (RegisterIntegrationSubscriptionInput, error) {
	integration, definition, err := subscriptionDefinition(
		ctx, dbsqlc.New(tx), projectID, attachment.IntegrationID,
	)
	if err != nil {
		return RegisterIntegrationSubscriptionInput{}, err
	}
	prepared, err := definition.Prepare(attachment.Conversation)
	if err != nil {
		return RegisterIntegrationSubscriptionInput{}, storeerr.InvalidRequest(err)
	}
	kind, ref, err := prepared.Conversation()
	if err != nil {
		return RegisterIntegrationSubscriptionInput{}, storeerr.InvalidRequest(err)
	}
	return RegisterIntegrationSubscriptionInput{
		OrgID: integration.OrgID, ProjectID: projectID, IntegrationID: integration.ID,
		Address: ConversationAddress{Kind: kind, Ref: ref},
	}, nil
}

func RegisterIntegrationSubscriptionTx(
	ctx context.Context,
	tx pgx.Tx,
	input RegisterIntegrationSubscriptionInput,
) (IntegrationSubscriptionRecord, error) {
	rows, err := RegisterIntegrationSubscriptionsTx(ctx, tx, []RegisterIntegrationSubscriptionInput{input})
	if err != nil {
		return IntegrationSubscriptionRecord{}, err
	}
	return rows[0], nil
}

func RegisterIntegrationSubscriptionsTx(
	ctx context.Context,
	tx pgx.Tx,
	inputs []RegisterIntegrationSubscriptionInput,
) ([]IntegrationSubscriptionRecord, error) {
	if len(inputs) == 0 {
		return nil, nil
	}
	q := dbsqlc.New(tx)
	first := inputs[0]
	if first.OrgID == uuid.Nil || first.ProjectID == uuid.Nil || first.AgentID == uuid.Nil {
		return nil, storeerr.InvalidRequest(errors.New("organization, project and agent are required"))
	}
	agent, err := q.GetAgentInProject(ctx, dbsqlc.GetAgentInProjectParams{ProjectID: first.ProjectID, ID: first.AgentID})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, storeerr.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if agent.State != "active" || agent.OrgID != first.OrgID {
		return nil, storeerr.ErrStateTransitionConflict
	}
	result := make([]IntegrationSubscriptionRecord, 0, len(inputs))
	inserted := false
	for _, input := range inputs {
		if input.OrgID != first.OrgID || input.ProjectID != first.ProjectID || input.AgentID != first.AgentID {
			return nil, storeerr.InvalidRequest(errors.New("subscription batch must belong to one agent"))
		}
		record, created, err := registerIntegrationSubscriptionTx(ctx, q, input)
		if err != nil {
			return nil, err
		}
		inserted = inserted || created
		record.AgentName = agent.Name
		result = append(result, record)
	}
	// A lowered quota must not invalidate replay of an existing subscription.
	if !inserted {
		return result, nil
	}
	limits, err := resourceguard.ResolveLimits(ctx, q, first.OrgID)
	if err != nil {
		return nil, err
	}
	count, err := q.CountAgentIntegrationSubscriptions(ctx, dbsqlc.CountAgentIntegrationSubscriptionsParams{
		ProjectID: first.ProjectID, AgentID: first.AgentID,
	})
	if err != nil {
		return nil, err
	}
	if count > limits.MaxActiveIntegrationSubscriptionsPerAgent {
		return nil, fmt.Errorf("integration subscriptions limit of %d reached: %w",
			limits.MaxActiveIntegrationSubscriptionsPerAgent, storeerr.ErrConflict)
	}
	return result, nil
}

func registerIntegrationSubscriptionTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input RegisterIntegrationSubscriptionInput,
) (IntegrationSubscriptionRecord, bool, error) {
	integration, _, err := subscriptionDefinition(ctx, q, input.ProjectID, input.IntegrationID)
	if err != nil {
		return IntegrationSubscriptionRecord{}, false, err
	}
	if integration.OrgID != input.OrgID {
		return IntegrationSubscriptionRecord{}, false, storeerr.ErrUnauthorized
	}
	if err := input.Address.Validate(); err != nil {
		return IntegrationSubscriptionRecord{}, false, err
	}
	scope, err := integrationdefinition.ParseConversation(integration.Provider, input.Address.Kind, input.Address.Ref)
	if err != nil {
		return IntegrationSubscriptionRecord{}, false, storeerr.InvalidRequest(err)
	}
	kind, ref, err := scope.Conversation()
	if err != nil || kind != input.Address.Kind || ref != input.Address.Ref {
		return IntegrationSubscriptionRecord{}, false, storeerr.InvalidRequest(
			errors.New("subscription address must be canonical"),
		)
	}
	conversation, err := scope.ConversationJSON()
	if err != nil {
		return IntegrationSubscriptionRecord{}, false, err
	}
	row, err := q.GetIntegrationSubscriptionForConversation(ctx, dbsqlc.GetIntegrationSubscriptionForConversationParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
		ScopeKind: input.Address.Kind, ScopeRef: input.Address.Ref,
	})
	created := errors.Is(err, pgx.ErrNoRows)
	if created {
		row, err = q.InsertIntegrationSubscription(ctx, dbsqlc.InsertIntegrationSubscriptionParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationID: input.IntegrationID,
			ScopeKind: input.Address.Kind, ScopeRef: input.Address.Ref,
		})
	}
	if err != nil {
		return IntegrationSubscriptionRecord{}, false, err
	}
	record := integrationSubscriptionRecord(row)
	record.Conversation = conversation
	return record, created, nil
}

func subscriptionDefinition(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, integrationID uuid.UUID,
) (ProjectIntegrationRecord, integrationdefinition.SubscriptionDefinition, error) {
	integration, err := getProjectIntegration(ctx, q, projectID, integrationID)
	if err != nil {
		return ProjectIntegrationRecord{}, integrationdefinition.SubscriptionDefinition{}, err
	}
	if integration.State != ProjectIntegrationStateActive {
		return integration, integrationdefinition.SubscriptionDefinition{}, storeerr.ErrUnauthorized
	}
	definition, found := integrationdefinition.Lookup(integration.IntegrationType)
	if !found || definition.Subscription == nil {
		return integration, integrationdefinition.SubscriptionDefinition{}, storeerr.InvalidRequest(
			errors.New("integration does not support subscriptions"),
		)
	}
	return integration, *definition.Subscription, nil
}

func integrationSubscriptionRecord(row dbsqlc.IntegrationSubscription) IntegrationSubscriptionRecord {
	return IntegrationSubscriptionRecord{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, IntegrationID: row.IntegrationID,
		Address:   ConversationAddress{Kind: row.ScopeKind, Ref: row.ScopeRef},
		CreatedAt: row.CreatedAt,
	}
}
