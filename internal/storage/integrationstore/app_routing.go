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

// AppRoutingCandidates is a snapshot for planning only. Admission rechecks the
// live subscription or launcher authority in its transaction.
type AppRoutingCandidates struct {
	Launcher      *ProjectAppRecord
	Subscriptions []AppSubscriptionRecord
	Selections    []IntegrationTargetRecord
}

// AppRoutingCandidatesForInbox reads routing in the caller's fenced receipt
// transaction. The lease has already acquired project/app/receipt gates;
// callers visiting multiple conversations must supply them in canonical order.
// No agent lock or provider I/O belongs in this planning transaction.
func (s *Store) AppRoutingCandidatesForInbox(
	ctx context.Context,
	work *IntegrationInboxLeaseTx,
	conversation ConversationAddress,
	scopes []ConversationAddress,
	event string,
) (AppRoutingCandidates, error) {
	if work == nil {
		return AppRoutingCandidates{}, storeerr.InvalidRequest(errors.New("inbox lease transaction is required"))
	}
	if err := work.checkLease(ctx); err != nil {
		return AppRoutingCandidates{}, err
	}
	if err := LockConversationTx(
		ctx,
		work.tx,
		work.record.ProjectID,
		work.record.AppID,
		conversation,
	); err != nil {
		return AppRoutingCandidates{}, err
	}
	// Wall time may have advanced while waiting for a prior launch/planner.
	if err := work.checkLease(ctx); err != nil {
		return AppRoutingCandidates{}, err
	}
	return s.AppRoutingCandidatesTx(
		ctx,
		work.tx,
		work.record.ProjectID,
		work.record.AppID,
		conversation,
		scopes,
		event,
	)
}

// AppRoutingCandidatesTx reads under the caller's app and conversation
// gates. Provider decoding supplies the exact conversation and bounded parent
// addresses; there is no user-authored SQL/filter language.
func (s *Store) AppRoutingCandidatesTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, appID uuid.UUID,
	conversation ConversationAddress,
	scopes []ConversationAddress,
	event string,
) (AppRoutingCandidates, error) {
	if len(scopes) == 0 || len(scopes) > 8 || event == "" {
		return AppRoutingCandidates{}, storeerr.InvalidRequest(
			errors.New("one to eight event scopes and an event are required"),
		)
	}
	if err := conversation.Validate(); err != nil {
		return AppRoutingCandidates{}, err
	}
	unique := make([]ConversationAddress, 0, len(scopes))
	seen := make(map[ConversationAddress]bool, len(scopes))
	for _, scope := range scopes {
		if err := scope.Validate(); err != nil {
			return AppRoutingCandidates{}, err
		}
		if !seen[scope] {
			seen[scope] = true
			unique = append(unique, scope)
		}
	}
	app, err := getProjectApp(ctx, dbsqlc.New(tx), projectID, appID)
	if err != nil {
		return AppRoutingCandidates{}, err
	}
	if app.State != ProjectAppStateActive {
		return AppRoutingCandidates{}, storeerr.ErrUnauthorized
	}
	q := dbsqlc.New(tx)
	result := AppRoutingCandidates{}
	if launcher := app.Settings.Launcher; launcher != nil {
		// The app lookup above is the boundary. Discord permits mentions in every
		// server where this bot is installed; subscriptions stay conversation-scoped.
		if app.Provider == IntegrationProviderDiscord && launcher.ScopeKind == "" && launcher.ScopeRef == "" {
			result.Launcher = &app
		}
		for _, scope := range unique {
			if launcher.ScopeKind == scope.Kind && launcher.ScopeRef == scope.Ref {
				result.Launcher = &app
				break
			}
		}
	}
	rawScopes, err := json.Marshal(unique)
	if err != nil {
		return AppRoutingCandidates{}, err
	}
	subscriptions, err := q.ListMatchingAppSubscriptions(
		ctx,
		dbsqlc.ListMatchingAppSubscriptionsParams{
			ProjectID: projectID,
			AppID:     appID,
			Scopes:    rawScopes,
			Event:     event,
		},
	)
	if err != nil {
		return AppRoutingCandidates{}, err
	}
	for _, row := range subscriptions {
		result.Subscriptions = append(result.Subscriptions, appSubscriptionRecord(row))
	}
	selections, err := q.ListConversationSelections(
		ctx,
		dbsqlc.ListConversationSelectionsParams{
			ProjectID: projectID,
			AppID:     appID,
			Kind:      conversation.Kind,
			Ref:       conversation.Ref,
		},
	)
	if err != nil {
		return AppRoutingCandidates{}, err
	}
	for _, row := range selections {
		result.Selections = append(
			result.Selections,
			appTargetRecord(dbsqlc.GetAgentConversationTargetRow(row), app.OrgID),
		)
	}
	return result, nil
}
