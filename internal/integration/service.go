package integration

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type Service struct {
	execution    executionStore
	integrations integrationStore
}

type executionStore interface {
	GetAgentProfile(context.Context, executionstore.ID, executionstore.ID) (executionstore.AgentProfileRecord, error)
	LaunchAgent(context.Context, executionstore.LaunchAgentInput) (executionstore.LaunchAgentResult, error)
}

type integrationStore interface {
	GetIntegrationInstallByID(context.Context, integrationstore.ID) (integrationstore.IntegrationInstallRecord, error)
	GetIntegrationTargetByProviderRef(
		context.Context,
		integrationstore.ID,
		integrationstore.ID,
		string,
	) (integrationstore.IntegrationTargetRecord, error)
	CreateIntegrationTarget(
		context.Context,
		integrationstore.CreateIntegrationTargetInput,
	) (integrationstore.IntegrationTargetRecord, error)
}

func New(execution executionStore, integrations integrationStore) *Service {
	return &Service{execution: execution, integrations: integrations}
}

type GetOrCreateTargetInput struct {
	IntegrationInstallID integrationstore.ID
	ProviderRef          string
	ProviderRefKind      string
	DisplayName          string
}

func (s *Service) GetOrCreateTarget(
	ctx context.Context,
	input GetOrCreateTargetInput,
) (integrationstore.IntegrationTargetRecord, executionstore.LaunchAgentResult, error) {
	if input.IntegrationInstallID == integrationstore.NilID ||
		input.ProviderRef == "" || input.ProviderRefKind == "" {
		return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, errors.New(
			"integration install, provider ref, and provider ref kind are required",
		)
	}
	install, err := s.integrations.GetIntegrationInstallByID(ctx, input.IntegrationInstallID)
	if err != nil {
		return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, err
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, storeerr.ErrUnauthorized
	}
	if existing, err := s.integrations.GetIntegrationTargetByProviderRef(
		ctx,
		install.ProjectID,
		install.ID,
		input.ProviderRef,
	); err == nil {
		if existing.ProviderRefKind != input.ProviderRefKind {
			return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, storeerr.ErrConflict
		}
		return existing, executionstore.LaunchAgentResult{}, nil
	} else if !storeerr.IsNotFound(err) {
		return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, err
	}

	agentID := install.AgentID
	var launch executionstore.LaunchAgentResult
	if agentID == integrationstore.NilID {
		profile, err := s.execution.GetAgentProfile(ctx, install.ProjectID, install.AgentProfileID)
		if err != nil {
			return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, err
		}
		launch, err = s.execution.LaunchAgent(ctx, executionstore.LaunchAgentInput{
			ProjectID:      install.ProjectID,
			ProfileID:      install.AgentProfileID,
			AgentConfigID:  profile.CurrentConfigID,
			LaunchedBy:     identitystore.NewUserPrincipal(install.InstalledByUserID),
			IdempotencyKey: "integration:" + install.Provider + ":" + install.ID.String() + ":" + input.ProviderRef,
		})
		if err != nil {
			return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, fmt.Errorf(
				"launch agent for integration target: %w",
				err,
			)
		}
		agentID = launch.Agent.ID
	}
	target, err := s.integrations.CreateIntegrationTarget(
		ctx,
		integrationstore.CreateIntegrationTargetInput{
			ProjectID:            install.ProjectID,
			AgentID:              agentID,
			IntegrationInstallID: install.ID,
			ProviderRef:          input.ProviderRef,
			ProviderRefKind:      input.ProviderRefKind,
			DisplayName:          strings.TrimSpace(input.DisplayName),
		},
	)
	if err != nil {
		return integrationstore.IntegrationTargetRecord{}, executionstore.LaunchAgentResult{}, err
	}
	return target, launch, nil
}
