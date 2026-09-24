package integrationstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationRoutingCandidates struct {
	Launcher      *ProjectIntegrationRecord
	Subscriptions []IntegrationSubscriptionRecord
	Selections    []IntegrationTargetRecord
}

func (s *Store) IntegrationRoutingCandidatesForInbox(
	ctx context.Context,
	work *IntegrationInboxLeaseTx,
	conversation ConversationAddress,
	scopes []ConversationAddress,
	event string,
) (IntegrationRoutingCandidates, error) {
	if work == nil {
		return IntegrationRoutingCandidates{}, storeerr.InvalidRequest(errors.New("inbox lease transaction is required"))
	}
	if err := work.checkLease(ctx); err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	if err := LockConversationTx(
		ctx,
		work.tx,
		work.record.ProjectID,
		work.record.IntegrationID,
		conversation,
	); err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	if err := work.checkLease(ctx); err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	return s.IntegrationRoutingCandidatesTx(
		ctx,
		work.tx,
		work.record.ProjectID,
		work.record.IntegrationID,
		conversation,
		scopes,
		event,
	)
}

func (s *Store) IntegrationRoutingCandidatesTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationID uuid.UUID,
	conversation ConversationAddress,
	scopes []ConversationAddress,
	event string,
) (IntegrationRoutingCandidates, error) {
	if len(scopes) == 0 || len(scopes) > 8 || event == "" {
		return IntegrationRoutingCandidates{}, storeerr.InvalidRequest(
			errors.New("one to eight event scopes and an event are required"),
		)
	}
	if err := conversation.Validate(); err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	unique := make([]ConversationAddress, 0, len(scopes))
	seen := make(map[ConversationAddress]bool, len(scopes))
	for _, scope := range scopes {
		if err := scope.Validate(); err != nil {
			return IntegrationRoutingCandidates{}, err
		}
		if !seen[scope] {
			seen[scope] = true
			unique = append(unique, scope)
		}
	}
	integration, err := getProjectIntegration(ctx, dbsqlc.New(tx), projectID, integrationID)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	if integration.State != ProjectIntegrationStateActive {
		return IntegrationRoutingCandidates{}, storeerr.ErrUnauthorized
	}
	q := dbsqlc.New(tx)
	result := IntegrationRoutingCandidates{}
	if launcher := integration.Settings.Launcher; launcher != nil {
		if integration.Provider == IntegrationProviderDiscord && launcher.ScopeKind == "" && launcher.ScopeRef == "" {
			result.Launcher = &integration
		}
		for _, scope := range unique {
			if launcher.ScopeKind == scope.Kind && launcher.ScopeRef == scope.Ref {
				result.Launcher = &integration
				break
			}
		}
	}
	rawScopes, err := json.Marshal(unique)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	subscriptions, err := q.ListMatchingIntegrationSubscriptions(
		ctx,
		dbsqlc.ListMatchingIntegrationSubscriptionsParams{
			ProjectID:     projectID,
			IntegrationID: integrationID,
			Scopes:        rawScopes,
			Event:         event,
		},
	)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	for _, row := range subscriptions {
		result.Subscriptions = append(result.Subscriptions, integrationSubscriptionRecord(row))
	}
	selections, err := q.ListConversationSelections(
		ctx,
		dbsqlc.ListConversationSelectionsParams{
			ProjectID:     projectID,
			IntegrationID: integrationID,
			Kind:          conversation.Kind,
			Ref:           conversation.Ref,
		},
	)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	for _, row := range selections {
		result.Selections = append(
			result.Selections,
			integrationTargetRecord(dbsqlc.GetAgentConversationTargetRow(row), integration.OrgID),
		)
	}
	return result, nil
}
