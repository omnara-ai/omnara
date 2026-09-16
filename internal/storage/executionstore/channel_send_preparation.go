package executionstore

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/channelconnector"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// PrepareChannelSend validates the persisted tool's params against the current
// definition under the final dispatch locks before any remote request is dispatched.
func (s *Store) PrepareChannelSend(
	ctx context.Context,
	prepared PreparedChannelOperation,
) (integrationstore.ChannelAccess, json.RawMessage, error) {
	var empty integrationstore.ChannelAccess
	if prepared.store != s || prepared.owner.Operation != integrationstore.ChannelBindingOperationSend {
		return empty, nil, storeerr.ErrUnauthorized
	}
	// Warm the bounded schema cache before taking database locks. Recheck below
	// still validates the actual current definition if it changed meanwhile.
	if _, err := jsonschema.Compile(prepared.access.SendParamsSchema); err != nil {
		return empty, nil, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return empty, nil, err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	access, call, err := s.recheckChannelOperationTx(ctx, tx, prepared)
	if err != nil {
		return empty, nil, err
	}
	// Ordinary tool admission validated the full message shape. Project only
	// these immutable arguments here; params remain raw, without number coercion.
	var input struct {
		ChannelID string          `json:"channel_id"`
		Params    json.RawMessage `json:"params"`
	}
	if err := json.Unmarshal(call.Input, &input); err != nil {
		return empty, nil, storeerr.InvalidRequest(err)
	}
	channelID, err := publicid.Decode(publicid.KindIntegrationTarget, input.ChannelID)
	if err != nil || channelID != access.ChannelID {
		return empty, nil, storeerr.ErrUnauthorized
	}
	params, err := channelconnector.ValidateSendParams(access.SendParamsSchema, input.Params)
	if err != nil {
		return empty, nil, storeerr.InvalidRequest(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return empty, nil, err
	}
	return access, params, nil
}
