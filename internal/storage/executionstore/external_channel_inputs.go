package executionstore

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func (s *Store) resolveExternalInputChannelTx(
	ctx context.Context, tx pgx.Tx, q *dbsqlc.Queries, input CreateAgentContentInputInput,
) (CreateAgentContentInputInput, *createAgentContentInputTxResult, error) {
	if !isNilID(input.IntegrationTargetID) || !isNilID(input.IntegrationTargetBindingID) {
		return input, nil, storeerr.InvalidRequest(errors.New("channel_id cannot be combined with a resolved input origin"))
	}
	input.IntegrationTargetID = input.ChannelID
	// An accepted input is immutable; replay needs no current channel authority.
	if replay, found, err := replayAgentContentInputTx(ctx, tx, q, input); err != nil || found {
		if err != nil {
			return input, nil, err
		}
		return input, &replay, nil
	}
	target, err := s.integrations.GetIntegrationTargetTx(ctx, tx, input.ProjectID, input.ChannelID)
	if err != nil {
		return input, nil, err
	}
	install, err := s.integrations.GetIntegrationInstallByIDTx(ctx, tx, target.IntegrationInstallID)
	if err != nil {
		return input, nil, err
	}
	if install.ProjectID != input.ProjectID {
		return input, nil, storeerr.ErrNotFound
	}
	if install.IntegrationKind != integrationstore.IntegrationKindExternal {
		return input, nil, storeerr.InvalidRequest(errors.New(
			"channel_id input origins require an external connection; managed channels use verified provider intake"))
	}
	// The installation gate precedes the agent lock, matching deletion. Check
	// replay again after waiting so a concurrent retry cannot require new grants.
	if err := lockIntegrationInputAgentTx(ctx, tx, install, input.AgentID); err != nil {
		return input, nil, err
	}
	if replay, found, err := replayAgentContentInputTx(ctx, tx, q, input); err != nil || found {
		if err != nil {
			return input, nil, err
		}
		return input, &replay, nil
	}
	binding, err := s.integrations.GetActiveReceiveBindingForTargetTx(
		ctx, tx, input.ProjectID, input.AgentID, input.ChannelID)
	if err != nil {
		return input, nil, err
	}
	// Selection is only discovery. Retain and recheck the exact live authority
	// chain through commit so concurrent revocation cannot admit a fresh input.
	binding, err = s.integrations.GetActiveReceiveBindingTx(
		ctx, tx, input.ProjectID, input.AgentID, install.ID, target.ID, binding.ID,
	)
	if err != nil {
		return input, nil, err
	}
	input.IntegrationTargetBindingID = binding.ID
	return input, nil, nil
}
