package integrationstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// RegisterExternalChannel fences public project-management registration to a
// live external connection. Managed addresses use their connector-owned path.
func (s *Store) RegisterExternalChannel(
	ctx context.Context,
	input CreateIntegrationTargetInput,
) (IntegrationTargetRecord, error) {
	// Kind and project ownership are immutable. The creator rechecks the live
	// installation and definition under its own transaction's lifecycle locks.
	install, err := s.GetIntegrationInstall(ctx, input.ProjectID, input.IntegrationInstallID)
	if err != nil {
		return IntegrationTargetRecord{}, err
	}
	if install.IntegrationKind != IntegrationKindExternal {
		return IntegrationTargetRecord{}, storeerr.ErrNotFound
	}
	return s.CreateIntegrationTarget(ctx, input)
}

// RevokeAgentChannelBinding checks immutable ownership, including revoked rows.
// Deleting an old binding again never affects a replacement for the same channel.
func (s *Store) RevokeAgentChannelBinding(ctx context.Context, projectID, agentID, id ID) error {
	if isNilID(projectID) || isNilID(agentID) || isNilID(id) {
		return storeerr.InvalidRequest(errors.New("project, agent and binding are required"))
	}
	belongs, err := s.q.IntegrationTargetBindingBelongsToAgent(ctx,
		dbsqlc.IntegrationTargetBindingBelongsToAgentParams{ProjectID: projectID, AgentID: agentID, ID: id})
	if err != nil {
		return fmt.Errorf("check agent channel binding owner: %w", err)
	}
	if !belongs {
		return storeerr.ErrNotFound
	}
	return s.RevokeIntegrationTargetBinding(ctx, projectID, id)
}
