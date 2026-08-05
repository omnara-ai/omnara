package integration

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

type integrationServiceExecutionStub struct{}

func (integrationServiceExecutionStub) GetAgentProfile(
	context.Context,
	executionstore.ID,
	executionstore.ID,
) (executionstore.AgentProfileRecord, error) {
	return executionstore.AgentProfileRecord{}, errors.New("unexpected GetAgentProfile call")
}

func (integrationServiceExecutionStub) LaunchAgent(
	context.Context,
	executionstore.LaunchAgentInput,
) (executionstore.LaunchAgentResult, error) {
	return executionstore.LaunchAgentResult{}, errors.New("unexpected LaunchAgent call")
}

type integrationServiceStoreStub struct {
	install       integrationstore.IntegrationInstallRecord
	lookupErr     error
	createdInput  integrationstore.CreateIntegrationTargetInput
	createdTarget integrationstore.IntegrationTargetRecord
}

func (s *integrationServiceStoreStub) GetIntegrationInstallByID(
	context.Context,
	integrationstore.ID,
) (integrationstore.IntegrationInstallRecord, error) {
	return s.install, nil
}

func (s *integrationServiceStoreStub) GetIntegrationTargetByProviderRef(
	context.Context,
	integrationstore.ID,
	integrationstore.ID,
	string,
) (integrationstore.IntegrationTargetRecord, error) {
	return integrationstore.IntegrationTargetRecord{}, s.lookupErr
}

func (s *integrationServiceStoreStub) CreateIntegrationTarget(
	_ context.Context,
	input integrationstore.CreateIntegrationTargetInput,
) (integrationstore.IntegrationTargetRecord, error) {
	s.createdInput = input
	return s.createdTarget, nil
}

func TestGetOrCreateTargetTreatsDatabaseNoRowsAsNotFound(t *testing.T) {
	installID := uuid.New()
	projectID := uuid.New()
	agentID := uuid.New()
	target := integrationstore.IntegrationTargetRecord{ID: uuid.New()}
	store := &integrationServiceStoreStub{
		install: integrationstore.IntegrationInstallRecord{
			ID:        installID,
			ProjectID: projectID,
			AgentID:   agentID,
			State:     integrationstore.IntegrationInstallStateActive,
		},
		lookupErr:     pgx.ErrNoRows,
		createdTarget: target,
	}

	got, launch, err := New(integrationServiceExecutionStub{}, store).GetOrCreateTarget(
		context.Background(),
		GetOrCreateTargetInput{
			IntegrationInstallID: installID,
			ProviderRef:          "provider-ref",
			ProviderRefKind:      "channel",
			DisplayName:          "  Release channel  ",
		},
	)
	if err != nil {
		t.Fatalf("get or create target: %v", err)
	}
	if got.ID != target.ID || launch.Created {
		t.Fatalf("result = (%+v, %+v), want existing agent target creation", got, launch)
	}
	wantInput := integrationstore.CreateIntegrationTargetInput{
		ProjectID:            projectID,
		AgentID:              agentID,
		IntegrationInstallID: installID,
		ProviderRef:          "provider-ref",
		ProviderRefKind:      "channel",
		DisplayName:          "Release channel",
	}
	if store.createdInput != wantInput {
		t.Fatalf("create input = %+v, want %+v", store.createdInput, wantInput)
	}
}
