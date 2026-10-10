package executionstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ActivateAgentConfigInput struct {
	ProjectID      uuid.UUID
	AgentID        uuid.UUID
	AgentConfigID  uuid.UUID
	ActorType      string
	ActorID        uuid.UUID
	Reason         string
	IdempotencyKey string
}

type AgentConfigChangeRecord struct {
	AgentInput AgentInputRecord
	Event      events.Event
}

type ChangeAgentConfigInput struct {
	CreateAgentConfigInput
	AgentID                 uuid.UUID
	ExpectedCurrentConfigID uuid.UUID
	ActorType               string
	ActorID                 uuid.UUID
	Reason                  string
	IdempotencyKey          string
}

type ChangeAgentConfigResult struct {
	AgentConfig    AgentConfigRecord
	ConfigChange   AgentConfigChangeRecord
	DeleteMachines []MachineRecord
}

func (s *Store) ChangeAgentConfig(
	ctx context.Context,
	input ChangeAgentConfigInput,
) (ChangeAgentConfigResult, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil {
		return ChangeAgentConfigResult{}, errors.New("project and agent are required")
	}
	return storeutil.RetryTransaction(ctx, "change_agent_config", func() (ChangeAgentConfigResult, error) {
		return s.changeAgentConfigOnce(ctx, input)
	})
}

func (s *Store) changeAgentConfigOnce(
	ctx context.Context,
	input ChangeAgentConfigInput,
) (ChangeAgentConfigResult, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return ChangeAgentConfigResult{}, fmt.Errorf("begin change agent config: %w", err)
	}
	defer func() { _ = unit.Rollback(ctx) }()
	tx := unit.DB()
	txNotifications := unit.Notifications()
	qtx := dbsqlc.New(tx)
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if err := lockConfigChangeIntegrationsTx(ctx, tx, qtx, input); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if err := qtx.LockAgentMachineSources(
		ctx,
		dbsqlc.LockAgentMachineSourcesParams{AgentID: input.AgentID},
	); err != nil {
		return ChangeAgentConfigResult{}, fmt.Errorf("lock agent machine sources for config change: %w", err)
	}
	agent, err := qtx.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	)
	if err != nil {
		return ChangeAgentConfigResult{}, fmt.Errorf("load agent for config change: %w", err)
	}
	if agent.ParentAgentID != nil {
		return ChangeAgentConfigResult{}, storeerr.InvalidRequest(
			errors.New("subagent configurations are read-only"),
		)
	}
	idempotentReplay, err := configChangeReplayExistsTx(ctx, qtx, input)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if !idempotentReplay && AgentState(agent.State) != AgentStateActive {
		return ChangeAgentConfigResult{}, storeerr.ErrStateTransitionConflict
	}
	configInput := input.CreateAgentConfigInput
	configInput.OrgID = project.OrgID
	configInput.ProjectID = input.ProjectID
	currentContract, nextContract, err := validateLiveAgentConfigChangeTx(
		ctx,
		qtx,
		input.ProjectID,
		agent.CurrentConfigID,
		configInput,
	)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	config, err := insertAgentConfigTx(ctx, qtx, configInput)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if !idempotentReplay && input.ExpectedCurrentConfigID != uuid.Nil &&
		agent.CurrentConfigID != input.ExpectedCurrentConfigID &&
		agent.CurrentConfigID != config.ID {
		return ChangeAgentConfigResult{}, fmt.Errorf(
			"agent current config changed: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	activationInput := ActivateAgentConfigInput{
		ProjectID:      input.ProjectID,
		AgentID:        input.AgentID,
		AgentConfigID:  config.ID,
		ActorType:      input.ActorType,
		ActorID:        input.ActorID,
		Reason:         input.Reason,
		IdempotencyKey: input.IdempotencyKey,
	}
	var nextSources []launchMachineSource
	if !idempotentReplay && !reflect.DeepEqual(currentContract.MachineSources, nextContract.MachineSources) {
		nextSources, err = decodeLaunchMachineSources(nextContract)
		if err != nil {
			return ChangeAgentConfigResult{}, err
		}
		if err := s.resolveLaunchPoolMachineSourcesTx(
			ctx,
			tx,
			qtx,
			project.OrgID,
			input.ProjectID,
			nextSources,
		); err != nil {
			return ChangeAgentConfigResult{}, err
		}
		attachedMachineIDs, err := qtx.ListAttachedAgentPoolMachineIDsForLifecycle(
			ctx,
			dbsqlc.ListAttachedAgentPoolMachineIDsForLifecycleParams{
				ProjectID: input.ProjectID,
				AgentID:   input.AgentID,
			},
		)
		if err != nil {
			return ChangeAgentConfigResult{}, fmt.Errorf(
				"list agent pool machines for config change: %w",
				err,
			)
		}
		if err := lockLaunchMachineSourcesTx(
			ctx,
			tx,
			project.OrgID,
			nextSources,
			attachedMachineIDs,
		); err != nil {
			return ChangeAgentConfigResult{}, err
		}
	}
	if err := lockAgentForConfigActivationTx(ctx, unit, activationInput); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if err := authorizeAgentConfigChangeTx(ctx, qtx, activationInput); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if len(nextSources) > 0 {
		if err := s.resolveLaunchExplicitMachineSourcesTx(
			ctx,
			qtx,
			input.ProjectID,
			nextSources,
		); err != nil {
			return ChangeAgentConfigResult{}, err
		}
	}
	configChange, err := activateLockedAuthorizedAgentConfigTx(
		ctx,
		unit,
		qtx,
		activationInput,
	)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	var deleteMachines []MachineRecord
	if !idempotentReplay && agent.CurrentConfigID != config.ID {
		currentAgent, err := qtx.GetAgentInProject(
			ctx,
			dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
		)
		if err != nil {
			return ChangeAgentConfigResult{}, fmt.Errorf("reload agent after config change: %w", err)
		}
		if currentAgent.CurrentConfigID == config.ID {
			if _, err := s.ReconcileInteractionSelectionInUnit(ctx, unit, input.ProjectID, input.AgentID); err != nil {
				return ChangeAgentConfigResult{}, err
			}
			deleteMachines, err = s.reconcileAgentMachineSourcesTx(
				ctx,
				txNotifications,
				unit,
				qtx,
				input.ProjectID,
				input.AgentID,
				currentContract,
				nextContract,
				nextSources,
			)
			if err != nil {
				return ChangeAgentConfigResult{}, err
			}
		}
	}
	if idempotentReplay {
		poolMachines, err := listPoolMachinesTx(ctx, qtx, input.ProjectID, input.AgentID)
		if err != nil {
			return ChangeAgentConfigResult{}, err
		}
		for _, poolMachine := range poolMachines {
			machine := poolMachine.Machine
			if machine.LifecycleState == MachineLifecycleStateDeleting &&
				machine.LifecycleReasonCode == "agent_config_machine_source_removed" {
				deleteMachines = append(deleteMachines, machine)
			}
		}
	}
	if err := unit.Commit(ctx, "change agent config"); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	return ChangeAgentConfigResult{
		AgentConfig:    config,
		ConfigChange:   configChange,
		DeleteMachines: deleteMachines,
	}, nil
}

func validateLiveAgentConfigChangeTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, currentConfigID uuid.UUID,
	next CreateAgentConfigInput,
) (agentconfig.RuntimeContract, agentconfig.RuntimeContract, error) {
	next = withDefaultAgentConfigCompilation(next)
	nextContract, err := agentconfig.RuntimeContractFromCompiled(
		next.CompiledDefinition,
		next.EffectiveDefinitionHash,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, fmt.Errorf(
			"load next agent config runtime contract: %w",
			err,
		)
	}
	if currentConfigID == uuid.Nil {
		return agentconfig.RuntimeContract{}, nextContract, nil
	}
	current, err := loadAgentConfigTx(ctx, qtx, projectID, currentConfigID)
	if err != nil {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, err
	}
	currentContract, err := agentconfig.RuntimeContractFromCompiled(
		current.CompiledDefinition,
		current.EffectiveDefinitionHash,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, fmt.Errorf(
			"load current agent config runtime contract: %w",
			err,
		)
	}
	return currentContract, nextContract, nil
}

func activateNewAgentConfigTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) (AgentConfigChangeRecord, error) {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.AgentConfigID == uuid.Nil {
		return AgentConfigChangeRecord{}, errors.New("project, agent, and config are required")
	}
	return activateLockedAuthorizedAgentConfigTx(ctx, unit, qtx, input)
}

func lockAgentForConfigActivationTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	input ActivateAgentConfigInput,
) error {
	if input.ProjectID == uuid.Nil || input.AgentID == uuid.Nil || input.AgentConfigID == uuid.Nil {
		return errors.New("project, agent, and config are required")
	}
	if _, err := unit.LockAgent(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
		agentexecution.IngressAuthority{}); err != nil {
		return fmt.Errorf("lock agent for config change: %w", err)
	}
	return nil
}

func authorizeAgentConfigChangeTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) error {
	if input.ActorType == "" || input.ActorType == identitystore.PrincipalTypeSystem {
		return nil
	}
	actorPrincipal := identitystore.PrincipalRecord{Type: input.ActorType, ID: input.ActorID}
	if !identitystore.IsAccountPrincipal(actorPrincipal) {
		return fmt.Errorf("unsupported agent config change actor type %q", input.ActorType)
	}
	return validateProjectPrincipalActionTx(
		ctx,
		qtx,
		input.ProjectID,
		actorPrincipal,
		identitystore.ProjectActionManage,
	)
}

func activateLockedAuthorizedAgentConfigTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) (AgentConfigChangeRecord, error) {
	if input.ActorType == "" {
		input.ActorType = identitystore.PrincipalTypeSystem
	}
	var actorID uuid.UUID
	actorPrincipal := identitystore.PrincipalRecord{Type: input.ActorType, ID: input.ActorID}
	switch {
	case input.ActorType == identitystore.PrincipalTypeSystem:
	case identitystore.IsAccountPrincipal(actorPrincipal):
		project, err := loadProjectTx(ctx, qtx, input.ProjectID)
		if err != nil {
			return AgentConfigChangeRecord{}, err
		}
		actorParams, err := OmnaraActorParams(project.OrgID, actorPrincipal)
		if err != nil {
			return AgentConfigChangeRecord{}, err
		}
		actorID, err = resolveActorTx(ctx, qtx, input.ProjectID, actorParams)
		if err != nil {
			return AgentConfigChangeRecord{}, err
		}
	default:
		return AgentConfigChangeRecord{}, fmt.Errorf(
			"unsupported agent config change actor type %q",
			input.ActorType,
		)
	}
	metadata, err := marshalJSON(
		map[string]any{"agent_config_id": input.AgentConfigID, "reason": input.Reason},
	)
	if err != nil {
		return AgentConfigChangeRecord{}, fmt.Errorf("marshal config change metadata: %w", err)
	}
	h, err := unit.Handle(input.ProjectID, input.AgentID)
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	result, err := h.ActivateConfig(ctx, agentexecution.ActivateConfigInput{
		ConfigID: input.AgentConfigID, ActorID: actorID, IdempotencyKey: input.IdempotencyKey, Metadata: metadata})
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	row, err := qtx.GetAgentInput(
		ctx,
		dbsqlc.GetAgentInputParams{ProjectID: input.ProjectID, AgentID: input.AgentID, ID: result.InputID},
	)
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	eventRow, err := qtx.GetEventByProjectAgentIdempotencyKey(
		ctx,
		dbsqlc.GetEventByProjectAgentIdempotencyKeyParams{
			ProjectID:      input.ProjectID,
			AgentID:        input.AgentID,
			IdempotencyKey: "agent_input:" + result.InputID.String()},
	)
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	event, err := eventFromProjectIdempotencySQLC(eventRow)
	return AgentConfigChangeRecord{AgentInput: agentInputRecordFromGetSQLC(row), Event: event}, err
}

func validateProjectPrincipalActionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID uuid.UUID,
	principal identitystore.PrincipalRecord,
	action string,
) error {
	if projectID == uuid.Nil {
		return errors.New("project id is required")
	}
	userID, orgAPIKeyID := identitystore.AccountPrincipalIDs(principal)
	if userID == nil && orgAPIKeyID == nil {
		return errors.New("principal is required")
	}
	roles, err := qtx.ListAgentInputProducerAuthorizationRoles(
		ctx,
		dbsqlc.ListAgentInputProducerAuthorizationRolesParams{
			ProjectID:   projectID,
			UserID:      userID,
			OrgApiKeyID: orgAPIKeyID,
		},
	)
	if err != nil {
		return fmt.Errorf("validate project principal action: %w", err)
	}
	if identitystore.ProjectRolesAllow(roles, action) {
		return nil
	}
	return storeerr.ErrUnauthorized
}
