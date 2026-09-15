package integrationstore

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ReplyChannelRegistration struct {
	Channel IntegrationTargetRecord
	Binding IntegrationTargetBindingRecord
}

// RegisterReplyChannelTx registers a provider continuation inside the owning
// request's completion transaction. The caller must resolve completed-request
// replay before calling it and fence its tool/interaction/runtime separately.
// Failure here does not undo or reclassify an already accepted provider send.
func (s *Store) RegisterReplyChannelTx(
	ctx context.Context,
	tx pgx.Tx,
	prepared IntegrationTargetBindingRecord,
	target CreateIntegrationTargetInput,
) (ReplyChannelRegistration, error) {
	if (target.ProjectID != uuid.Nil && target.ProjectID != prepared.ProjectID) ||
		(target.IntegrationInstallID != uuid.Nil && target.IntegrationInstallID != prepared.IntegrationInstallID) ||
		target.ParentChannelID != prepared.IntegrationTargetID || target.ChannelDefinitionID == uuid.Nil {
		return ReplyChannelRegistration{}, storeerr.InvalidRequest(errors.New("reply channel is outside the pinned scope"))
	}
	live, err := s.RecheckChannelBindingTx(ctx, tx, PrepareChannelBindingInput{
		ProjectID: prepared.ProjectID, AgentID: prepared.AgentID,
		IntegrationInstallID: prepared.IntegrationInstallID, IntegrationTargetID: prepared.IntegrationTargetID,
		Operation: ChannelBindingOperationSend, CreatesReplyChannel: true,
	}, prepared)
	if err != nil {
		return ReplyChannelRegistration{}, err
	}
	parent, err := s.GetIntegrationTargetTx(ctx, tx, live.ProjectID, live.IntegrationTargetID)
	if err != nil {
		return ReplyChannelRegistration{}, err
	}
	q := s.q.WithTx(tx)
	if _, err := q.LockChannelDefinition(ctx, dbsqlc.LockChannelDefinitionParams{
		ProjectID: live.ProjectID, IntegrationInstallID: live.IntegrationInstallID, ID: parent.ChannelDefinitionID,
	}); err != nil {
		return ReplyChannelRegistration{}, integrationChannelReadError("lock reply parent definition", err)
	}
	row, err := q.GetChannelDefinition(ctx, dbsqlc.GetChannelDefinitionParams{
		ProjectID: live.ProjectID, IntegrationInstallID: live.IntegrationInstallID, ID: parent.ChannelDefinitionID,
	})
	if err != nil {
		return ReplyChannelRegistration{}, integrationChannelReadError("get reply parent definition", err)
	}
	definition, err := channelDefinitionFromSQLC(row)
	if err != nil {
		return ReplyChannelRegistration{}, err
	}
	if !definition.Capabilities.Send || !definition.Capabilities.CreatesReplyChannel {
		return ReplyChannelRegistration{}, storeerr.ErrUnauthorized
	}
	target.ProjectID, target.IntegrationInstallID = live.ProjectID, live.IntegrationInstallID
	channel, err := s.CreateIntegrationTargetTx(ctx, tx, target)
	if err != nil {
		return ReplyChannelRegistration{}, err
	}
	grants := live.ReplyChannelGrants
	binding, err := s.InitialChannelBindingTx(ctx, tx, CreateIntegrationTargetBindingInput{
		ProjectID: live.ProjectID, AgentID: live.AgentID, IntegrationInstallID: live.IntegrationInstallID,
		IntegrationTargetID: channel.ID, IntegrationRouteID: live.IntegrationRouteID,
		ReceiveAllowed: grants.ReceiveAllowed, ReadAllowed: grants.ReadAllowed, SendAllowed: grants.SendAllowed,
		Source: "reply_channel",
	})
	if err != nil {
		return ReplyChannelRegistration{}, err
	}
	return ReplyChannelRegistration{Channel: channel, Binding: binding}, nil
}
