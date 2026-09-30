package integrationstore

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type IntegrationRoutingCandidates struct {
	Launcher      *IntegrationRecord
	Subscriptions []IntegrationSubscriptionRecord
	LaunchOwners  []IntegrationTargetRecord
}

func (s *Store) IntegrationRoutingCandidatesForInbox(
	ctx context.Context,
	work *IntegrationInboxLeaseTx,
	conversation ConversationAddress,
	scopes []ConversationAddress,
) (IntegrationRoutingCandidates, error) {
	if work == nil {
		return IntegrationRoutingCandidates{}, storeerr.InvalidRequest(errors.New("inbox lease transaction is required"))
	}
	if err := work.CheckLease(ctx); err != nil {
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
	if err := work.CheckLease(ctx); err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	return s.IntegrationRoutingCandidatesTx(
		ctx,
		work.tx,
		work.record.ProjectID,
		work.record.IntegrationID,
		conversation,
		scopes,
	)
}

func (s *Store) IntegrationRoutingCandidatesTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, integrationID uuid.UUID,
	conversation ConversationAddress,
	scopes []ConversationAddress,
) (IntegrationRoutingCandidates, error) {
	if len(scopes) == 0 || len(scopes) > 8 {
		return IntegrationRoutingCandidates{}, storeerr.InvalidRequest(
			errors.New("one to eight event scopes are required"),
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
	integration, err := getIntegration(ctx, dbsqlc.New(tx), projectID, integrationID)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	if integration.State != IntegrationStateActive {
		return IntegrationRoutingCandidates{}, storeerr.ErrUnauthorized
	}
	q := dbsqlc.New(tx)
	result := IntegrationRoutingCandidates{}
	if definition, ok := integrationdefinition.Lookup(integration.IntegrationKind); ok && definition.Launcher != nil {
		// The definition evaluates settings and event scope before planning a launch.
		result.Launcher = &integration
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
		},
	)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	for _, row := range subscriptions {
		result.Subscriptions = append(result.Subscriptions, integrationSubscriptionRecord(row))
	}
	owners, err := q.ListConversationLaunchOwners(
		ctx,
		dbsqlc.ListConversationLaunchOwnersParams{
			ProjectID:     projectID,
			IntegrationID: integrationID,
			Kind:          conversation.Kind,
			Ref:           conversation.Ref,
		},
	)
	if err != nil {
		return IntegrationRoutingCandidates{}, err
	}
	for _, row := range owners {
		result.LaunchOwners = append(
			result.LaunchOwners,
			integrationTargetRecord(dbsqlc.GetAgentConversationTargetRow(row), integration.OrgID),
		)
	}
	return result, nil
}

func (s *Store) HasIntegrationLaunchOwner(
	ctx context.Context, projectID, integrationID uuid.UUID, address ConversationAddress,
) (bool, error) {
	if err := address.Validate(); err != nil {
		return false, err
	}
	owners, err := s.q.ListConversationLaunchOwners(ctx, dbsqlc.ListConversationLaunchOwnersParams{
		ProjectID: projectID, IntegrationID: integrationID, Kind: address.Kind, Ref: address.Ref,
	})
	return len(owners) != 0, err
}
