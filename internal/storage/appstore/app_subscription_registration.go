package appstore

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/resourceguard"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func PrepareAppSubscriptionTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID uuid.UUID,
	attachment AppSubscriptionAttachment,
) (RegisterAppSubscriptionInput, error) {
	app, definition, err := subscriptionDefinition(ctx, dbsqlc.New(tx), projectID, attachment.AppID, attachment.Type)
	if err != nil {
		return RegisterAppSubscriptionInput{}, err
	}
	prepared, err := definition.Prepare(attachment.Conversation, attachment.Events)
	if err != nil {
		return RegisterAppSubscriptionInput{}, storeerr.InvalidRequest(err)
	}
	kind, ref, err := prepared.Scope.Conversation()
	if err != nil {
		return RegisterAppSubscriptionInput{}, storeerr.InvalidRequest(err)
	}
	return RegisterAppSubscriptionInput{
		OrgID: app.OrgID, ProjectID: projectID, AppID: app.ID, Type: attachment.Type,
		Address: ConversationAddress{Kind: kind, Ref: ref}, Events: prepared.Events,
	}, nil
}

func RegisterAppSubscriptionTx(
	ctx context.Context,
	tx pgx.Tx,
	input RegisterAppSubscriptionInput,
) (AppSubscriptionRecord, error) {
	rows, err := RegisterAppSubscriptionsTx(ctx, tx, []RegisterAppSubscriptionInput{input})
	if err != nil {
		return AppSubscriptionRecord{}, err
	}
	return rows[0], nil
}

func RegisterAppSubscriptionsTx(
	ctx context.Context,
	tx pgx.Tx,
	inputs []RegisterAppSubscriptionInput,
) ([]AppSubscriptionRecord, error) {
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
	result := make([]AppSubscriptionRecord, 0, len(inputs))
	inserted := false
	for _, input := range inputs {
		if input.OrgID != first.OrgID || input.ProjectID != first.ProjectID || input.AgentID != first.AgentID {
			return nil, storeerr.InvalidRequest(errors.New("subscription batch must belong to one agent"))
		}
		record, created, err := registerAppSubscriptionTx(ctx, q, input)
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
	count, err := q.CountAgentAppSubscriptions(ctx, dbsqlc.CountAgentAppSubscriptionsParams{
		ProjectID: first.ProjectID, AgentID: first.AgentID,
	})
	if err != nil {
		return nil, err
	}
	if count > limits.MaxActiveAppSubscriptionsPerAgent {
		return nil, fmt.Errorf("app subscriptions limit of %d reached: %w",
			limits.MaxActiveAppSubscriptionsPerAgent, storeerr.ErrConflict)
	}
	return result, nil
}

func registerAppSubscriptionTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	input RegisterAppSubscriptionInput,
) (AppSubscriptionRecord, bool, error) {
	app, definition, err := subscriptionDefinition(ctx, q, input.ProjectID, input.AppID, input.Type)
	if err != nil {
		return AppSubscriptionRecord{}, false, err
	}
	if app.OrgID != input.OrgID {
		return AppSubscriptionRecord{}, false, storeerr.ErrUnauthorized
	}
	if err := input.Address.Validate(); err != nil {
		return AppSubscriptionRecord{}, false, err
	}
	scope, err := appdefinition.ParseConversation(app.Provider, input.Address.Kind, input.Address.Ref)
	if err != nil {
		return AppSubscriptionRecord{}, false, storeerr.InvalidRequest(err)
	}
	kind, ref, err := scope.Conversation()
	if err != nil || kind != input.Address.Kind || ref != input.Address.Ref {
		return AppSubscriptionRecord{}, false, storeerr.InvalidRequest(errors.New("subscription address must be canonical"))
	}
	conversation, err := scope.ConversationJSON()
	if err != nil {
		return AppSubscriptionRecord{}, false, err
	}
	prepared, err := definition.Prepare(conversation, input.Events)
	if err != nil {
		return AppSubscriptionRecord{}, false, storeerr.InvalidRequest(err)
	}
	row, err := q.GetAppSubscriptionForConversation(ctx, dbsqlc.GetAppSubscriptionForConversationParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: input.AppID, SubscriptionType: input.Type,
		ScopeKind: input.Address.Kind, ScopeRef: input.Address.Ref,
	})
	created := errors.Is(err, pgx.ErrNoRows)
	if created {
		row, err = q.InsertAppSubscription(ctx, dbsqlc.InsertAppSubscriptionParams{
			ProjectID: input.ProjectID, AgentID: input.AgentID, AppID: input.AppID, SubscriptionType: input.Type,
			ScopeKind: input.Address.Kind, ScopeRef: input.Address.Ref, Events: prepared.Events,
		})
	}
	if err != nil {
		return AppSubscriptionRecord{}, false, err
	}
	if !slices.Equal(row.Events, prepared.Events) {
		return AppSubscriptionRecord{}, false, storeerr.Tag(storeerr.ErrConflict,
			errors.New("conversation already subscribed with different events; remove it before changing events"))
	}
	record := appSubscriptionRecord(row)
	record.Conversation = conversation
	return record, created, nil
}

func subscriptionDefinition(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, appID uuid.UUID,
	subscriptionType string,
) (ProjectAppRecord, appdefinition.SubscriptionDefinition, error) {
	app, err := getProjectApp(ctx, q, projectID, appID)
	if err != nil {
		return ProjectAppRecord{}, appdefinition.SubscriptionDefinition{}, err
	}
	if app.State != ProjectAppStateActive {
		return app, appdefinition.SubscriptionDefinition{}, storeerr.ErrUnauthorized
	}
	definition, found := appdefinition.Lookup(app.AppType)
	subscription, exported := definition.Subscriptions[subscriptionType]
	if !found || !exported {
		return app, subscription, storeerr.InvalidRequest(errors.New("app does not export this subscription type"))
	}
	return app, subscription, nil
}

func appSubscriptionRecord(row dbsqlc.AppSubscription) AppSubscriptionRecord {
	return AppSubscriptionRecord{
		ID: row.ID, ProjectID: row.ProjectID, AgentID: row.AgentID, AppID: row.AppID,
		Type: row.SubscriptionType, Address: ConversationAddress{Kind: row.ScopeKind, Ref: row.ScopeRef},
		Events: row.Events, CreatedAt: row.CreatedAt,
	}
}
