package integrationstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ChannelBindingOperation string

const (
	ChannelBindingOperationRead ChannelBindingOperation = "read"
	ChannelBindingOperationSend ChannelBindingOperation = "send"
)

type PrepareChannelBindingInput struct {
	ProjectID            uuid.UUID
	AgentID              uuid.UUID
	IntegrationInstallID uuid.UUID
	IntegrationTargetID  uuid.UUID
	Operation            ChannelBindingOperation
	CreatesReplyChannel  bool
}

// PrepareChannelBindingTx selects and locks one eligible binding in stable
// creation order. The caller must retain its identity and exact reply grants in
// the owning request, commit before provider I/O, and separately check current
// definition support. A returned binding is not a reusable authority token.
func (s *Store) PrepareChannelBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input PrepareChannelBindingInput,
) (IntegrationTargetBindingRecord, error) {
	return s.lockChannelOperationBinding(ctx, tx, input, uuid.Nil)
}

// RecheckChannelBindingTx fences completion against the same live binding and
// exact reply tuple. A revoked binding is never replaced by another candidate.
// Child registration delegates only the tuple's ordinary permissions, not this
// binding's ReplyChannelGrants field. Explicit later setup may grant delegation.
func (s *Store) RecheckChannelBindingTx(
	ctx context.Context,
	tx pgx.Tx,
	input PrepareChannelBindingInput,
	prepared IntegrationTargetBindingRecord,
) (IntegrationTargetBindingRecord, error) {
	if prepared.ID == uuid.Nil || prepared.ProjectID != input.ProjectID || prepared.AgentID != input.AgentID ||
		prepared.IntegrationInstallID != input.IntegrationInstallID ||
		prepared.IntegrationTargetID != input.IntegrationTargetID {
		return IntegrationTargetBindingRecord{}, storeerr.ErrUnauthorized
	}
	live, err := s.lockChannelOperationBinding(ctx, tx, input, prepared.ID)
	if err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if !sameChannelGrants(live.ReplyChannelGrants, prepared.ReplyChannelGrants) {
		return IntegrationTargetBindingRecord{}, storeerr.ErrUnauthorized
	}
	return live, nil
}

func (s *Store) lockChannelOperationBinding(
	ctx context.Context,
	tx pgx.Tx,
	input PrepareChannelBindingInput,
	bindingID uuid.UUID,
) (IntegrationTargetBindingRecord, error) {
	if tx == nil || input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil ||
		input.IntegrationInstallID == uuid.Nil || input.IntegrationTargetID == uuid.Nil {
		return IntegrationTargetBindingRecord{}, storeerr.InvalidRequest(
			errors.New("transaction, project, agent, connection, and channel are required"))
	}
	if (input.Operation != ChannelBindingOperationRead && input.Operation != ChannelBindingOperationSend) ||
		(input.CreatesReplyChannel && input.Operation != ChannelBindingOperationSend) {
		return IntegrationTargetBindingRecord{}, storeerr.InvalidRequest(errors.New("invalid channel binding operation"))
	}
	if _, err := lockIntegrationInstallLifecycleShared(ctx, tx, input.ProjectID, input.IntegrationInstallID); err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	if err := lifecyclelock.Agents(ctx, tx, []lifecyclelock.AgentRef{{
		ProjectID: input.ProjectID, AgentID: input.AgentID,
	}}); err != nil {
		return IntegrationTargetBindingRecord{}, err
	}
	row, err := s.q.WithTx(tx).LockChannelOperationBinding(ctx, dbsqlc.LockChannelOperationBindingParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IntegrationInstallID: input.IntegrationInstallID,
		IntegrationTargetID: input.IntegrationTargetID, Operation: string(input.Operation),
		CreatesReplyChannel: input.CreatesReplyChannel, BindingID: storeutil.IDFromNil(bindingID),
	})
	if err != nil {
		return IntegrationTargetBindingRecord{}, integrationChannelReadError("lock channel operation binding", err)
	}
	return integrationTargetBindingRecordFromSQLC(row), nil
}

func sameChannelGrants(a, b *ChannelGrants) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func channelGrantsFromSQLC(receive, read, send *bool) *ChannelGrants {
	if receive == nil {
		return nil
	}
	// The database requires either all three booleans or none.
	return &ChannelGrants{ReceiveAllowed: *receive, ReadAllowed: *read, SendAllowed: *send}
}
