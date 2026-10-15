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
	return agentexecution.Retry(ctx,
		"change_agent_config",
		func(ctx context.Context) (ChangeAgentConfigResult, error) {
			result, err := s.changeAgentConfigOnce(ctx, input)
			if isUniqueViolationOnConstraint(err, "agent_inputs_idempotency_idx") {
				err = storeutil.ErrRetryTransaction
			}
			return result, err
		})
}

func (s *Store) changeAgentConfigOnce(
	ctx context.Context,
	input ChangeAgentConfigInput,
) (ChangeAgentConfigResult, error) {
	unit, err := s.cell.Begin(ctx)
	if err != nil {
		return ChangeAgentConfigResult{}, err
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
	replay := func() (ChangeAgentConfigResult, bool, error) {
		stored, found, err := loadAgentInputByIdempotencyMaybeTx(ctx,
			tx,
			input.ProjectID,
			input.AgentID,
			"agent_config_change",
			input.IdempotencyKey)
		if err != nil || !found {
			return ChangeAgentConfigResult{}, false, err
		}
		result, err := replayAgentConfigChangeTx(ctx, unit, qtx, input, stored)
		return result, true, err
	}
	if result, replayed, err := replay(); replayed || err != nil {
		return result, err
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
	if result, replayed, err := replay(); replayed || err != nil {
		return result, err
	}
	agent, err := qtx.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	)
	if err != nil {
		return ChangeAgentConfigResult{}, fmt.Errorf("load agent for config change: %w", err)
	}
	if AgentState(agent.State) != AgentStateActive {
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
	config, err := insertAgentConfigTx(ctx, qtx, configInput, unit.TrackCreation)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if input.ExpectedCurrentConfigID != uuid.Nil &&
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
	if !reflect.DeepEqual(currentContract.MachineSources, nextContract.MachineSources) {
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
	if agent.CurrentConfigID != config.ID {
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
	if err := unit.Commit(ctx, "change agent config"); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	return ChangeAgentConfigResult{
		AgentConfig:    config,
		ConfigChange:   configChange,
		DeleteMachines: deleteMachines,
	}, nil
}

func replayAgentConfigChangeTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input ChangeAgentConfigInput,
	stored AgentInputRecord,
) (ChangeAgentConfigResult, error) {
	if err := authorizeAgentConfigChangeTx(ctx,
		qtx,
		ActivateAgentConfigInput{ProjectID: input.ProjectID,
			AgentID:       input.AgentID,
			AgentConfigID: stored.AgentConfigID,
			ActorType:     input.ActorType,
			ActorID:       input.ActorID}); err != nil {
		return ChangeAgentConfigResult{}, err
	}

	if stored.State != "resolved" {
		return ChangeAgentConfigResult{}, storeerr.ErrStateTransitionConflict
	}
	config, err := loadAgentConfigTx(ctx, qtx, input.ProjectID, stored.AgentConfigID)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	eventRow, err := qtx.GetEventByProjectAgentIdempotencyKey(ctx, dbsqlc.GetEventByProjectAgentIdempotencyKeyParams{
		ProjectID: input.ProjectID, AgentID: input.AgentID, IdempotencyKey: "agent_input:" + stored.ID.String(),
	})
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	event, err := eventFromProjectIdempotencySQLC(eventRow)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	poolMachines, err := listPoolMachinesTx(ctx, qtx, input.ProjectID, input.AgentID)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	var deleteMachines []MachineRecord
	for _, poolMachine := range poolMachines {
		machine := poolMachine.Machine
		if machine.LifecycleState == MachineLifecycleStateDeleting &&
			machine.LifecycleReasonCode == "agent_config_machine_source_removed" {
			deleteMachines = append(deleteMachines, machine)
		}
	}
	if err := unit.Commit(ctx, "replay agent config change"); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	return ChangeAgentConfigResult{
		AgentConfig:    config,
		ConfigChange:   AgentConfigChangeRecord{AgentInput: stored, Event: event},
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
	if _, err := lockExecution(ctx, unit, input.ProjectID, input.AgentID, false, uuid.Nil); err != nil {
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
