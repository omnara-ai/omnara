package executionstore

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ActivateAgentConfigInput struct {
	ProjectID      ID
	AgentID        ID
	AgentConfigID  ID
	ActorType      string
	ActorID        ID
	Reason         string
	IdempotencyKey string
}

type AgentConfigChangeRecord struct {
	AgentInput AgentInputRecord
	Event      events.Event
}

type ChangeAgentConfigInput struct {
	CreateAgentConfigInput
	AgentID        ID
	ActorType      string
	ActorID        ID
	Reason         string
	IdempotencyKey string
}

type ChangeAgentConfigResult struct {
	AgentConfig    AgentConfigRecord
	ConfigChange   AgentConfigChangeRecord
	DeleteMachines []MachineRecord
}

func (s *Store) ChangeAgentConfig(ctx context.Context, input ChangeAgentConfigInput) (ChangeAgentConfigResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) {
		return ChangeAgentConfigResult{}, errors.New("project and agent are required")
	}
	return storeutil.RetryTransaction(ctx, func() (ChangeAgentConfigResult, error) {
		return s.changeAgentConfigOnce(ctx, input)
	})
}

func (s *Store) changeAgentConfigOnce(
	ctx context.Context,
	input ChangeAgentConfigInput,
) (ChangeAgentConfigResult, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ChangeAgentConfigResult{}, fmt.Errorf("begin change agent config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
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
	activationInput := ActivateAgentConfigInput{
		ProjectID:      input.ProjectID,
		AgentID:        input.AgentID,
		AgentConfigID:  config.ID,
		ActorType:      input.ActorType,
		ActorID:        input.ActorID,
		Reason:         input.Reason,
		IdempotencyKey: input.IdempotencyKey,
	}
	idempotentReplay := false
	if input.IdempotencyKey != "" {
		_, replayErr := qtx.GetAgentInputByIdempotency(
			ctx,
			dbsqlc.GetAgentInputByIdempotencyParams{
				ProjectID:           input.ProjectID,
				AgentID:             input.AgentID,
				IdempotencyScope:    "agent_config_change",
				InputIdempotencyKey: input.IdempotencyKey,
			},
		)
		switch {
		case replayErr == nil:
			idempotentReplay = true
		case !errors.Is(replayErr, pgx.ErrNoRows):
			return ChangeAgentConfigResult{}, fmt.Errorf("load idempotent config change: %w", replayErr)
		}
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
			return ChangeAgentConfigResult{}, fmt.Errorf("list agent pool machines for config change: %w", err)
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
	if err := lockAgentForConfigActivationTx(ctx, qtx, activationInput); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if err := authorizeAgentConfigChangeTx(ctx, qtx, activationInput); err != nil {
		return ChangeAgentConfigResult{}, err
	}
	if len(nextSources) > 0 {
		if err := s.resolveLaunchExplicitMachineSourcesTx(
			ctx,
			qtx,
			project.OrgID,
			input.ProjectID,
			nextSources,
		); err != nil {
			return ChangeAgentConfigResult{}, err
		}
	}
	configChange, err := activateLockedAuthorizedAgentConfigTx(
		ctx,
		txNotifications,
		tx,
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
			deleteMachines, err = s.reconcileAgentMachineSourcesTx(
				ctx,
				txNotifications,
				tx,
				qtx,
				project.OrgID,
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
	if err := qtx.ReconcileAgentWakeup(ctx, dbsqlc.ReconcileAgentWakeupParams{
		ProjectID: input.ProjectID,
		AgentID:   input.AgentID,
		Metadata:  []byte(`{"reason":"config_change"}`),
	}); err != nil {
		return ChangeAgentConfigResult{}, fmt.Errorf("reconcile config-change wakeup: %w", err)
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "change agent config"); err != nil {
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
	projectID, currentConfigID ID,
	next CreateAgentConfigInput,
) (agentconfig.RuntimeContract, agentconfig.RuntimeContract, error) {
	next = withDefaultAgentConfigCompilation(next)
	if next.CompilerVersion != agentconfig.CompilerVersion {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, fmt.Errorf(
			"agent config compiler contract %q is not activatable: %w",
			next.CompilerVersion,
			storeerr.ErrStateTransitionConflict,
		)
	}
	nextContract, err := agentconfig.RuntimeContractFromCompiled(
		next.CompiledDefinition,
		next.CompilerVersion,
		next.EffectiveDefinitionHash,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, fmt.Errorf(
			"load next agent config runtime contract: %w",
			err,
		)
	}
	if isNilID(currentConfigID) {
		return agentconfig.RuntimeContract{}, nextContract, nil
	}
	current, err := loadAgentConfigTx(ctx, qtx, projectID, currentConfigID)
	if err != nil {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, err
	}
	currentContract, err := agentconfig.RuntimeContractFromCompiled(
		current.CompiledDefinition,
		current.CompilerVersion,
		current.EffectiveDefinitionHash,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, fmt.Errorf(
			"load current agent config runtime contract: %w",
			err,
		)
	}
	if !reflect.DeepEqual(currentContract.MCPServers, nextContract.MCPServers) {
		return agentconfig.RuntimeContract{}, agentconfig.RuntimeContract{}, storeerr.InvalidRequest(errors.New(
			"live config changes cannot change mcp declarations yet",
		))

	}
	return currentContract, nextContract, nil
}

func activateInitialAgentConfigTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) (AgentConfigChangeRecord, error) {
	if err := lockAgentForConfigActivationTx(ctx, qtx, input); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	return activateLockedAuthorizedAgentConfigTx(ctx, txNotifications, tx, qtx, input)
}

func lockAgentForConfigActivationTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) error {
	if isNilID(input.ProjectID) || isNilID(input.AgentID) || isNilID(input.AgentConfigID) {
		return errors.New("project, agent, and config are required")
	}
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: input.ProjectID, ID: input.AgentID},
	); err != nil {
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
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) (AgentConfigChangeRecord, error) {
	if input.ActorType == "" {
		input.ActorType = identitystore.PrincipalTypeSystem
	}
	var actorID ID
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
		actorID, err = resolveActorTx(ctx, qtx, input.ProjectID, input.AgentID, actorParams, NilID)
		if err != nil {
			return AgentConfigChangeRecord{}, err
		}
	default:
		return AgentConfigChangeRecord{}, fmt.Errorf("unsupported agent config change actor type %q", input.ActorType)
	}
	if _, err := qtx.GetAgentConfig(
		ctx,
		dbsqlc.GetAgentConfigParams{ProjectID: input.ProjectID, ID: input.AgentConfigID},
	); err != nil {
		return AgentConfigChangeRecord{}, fmt.Errorf("load agent config for change: %w", err)
	}
	metadata, err := marshalJSON(map[string]any{"agent_config_id": input.AgentConfigID, "reason": input.Reason})
	if err != nil {
		return AgentConfigChangeRecord{}, fmt.Errorf("marshal config change metadata: %w", err)
	}
	row, err := qtx.InsertConfigChangeAgentInput(ctx, dbsqlc.InsertConfigChangeAgentInputParams{
		AgentConfigID:       sqlcIDFromNil(input.AgentConfigID),
		ActorID:             sqlcIDFromNil(actorID),
		IdempotencyScope:    sqlcTextFromEmpty("agent_config_change"),
		InputIdempotencyKey: sqlcTextFromEmpty(input.IdempotencyKey),
		Metadata:            metadata,
		ProjectID:           input.ProjectID,
		AgentID:             input.AgentID,
	})
	if err != nil {
		return AgentConfigChangeRecord{}, fmt.Errorf("insert config-change agent input: %w", err)
	}
	agentInput := agentInputRecordFromConfigChangeSQLC(row)
	if agentInput.ActorID != actorID ||
		agentInput.AgentConfigID != input.AgentConfigID {
		return AgentConfigChangeRecord{}, storeerr.ErrIdempotencyConflict
	}
	if !sameJSON(agentInput.Metadata, metadata) {
		return AgentConfigChangeRecord{}, storeerr.ErrIdempotencyConflict
	}
	if agentInput.State == "resolved" {
		eventRow, err := qtx.GetEventByProjectAgentIdempotencyKey(
			ctx,
			dbsqlc.GetEventByProjectAgentIdempotencyKeyParams{
				ProjectID:      input.ProjectID,
				AgentID:        input.AgentID,
				IdempotencyKey: "agent_input:" + agentInput.ID.String(),
			},
		)
		if err != nil {
			return AgentConfigChangeRecord{}, fmt.Errorf("load idempotent config-change event: %w", err)
		}
		event, err := eventFromProjectIdempotencySQLC(eventRow)
		if err != nil {
			return AgentConfigChangeRecord{}, err
		}
		if agentInput.AdmittedEventID != event.ID {
			return AgentConfigChangeRecord{}, storeerr.ErrStateTransitionConflict
		}
		return AgentConfigChangeRecord{AgentInput: agentInput, Event: event}, nil
	}
	eventRecord, _, _, err := appendEventToCurrentOrNewAgentTurnTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		AppendTypedAgentEventInput{
			ProjectID:      input.ProjectID,
			AgentID:        input.AgentID,
			Kind:           events.KindAgentInput,
			IdempotencyKey: "agent_input:" + agentInput.ID.String(),
			AgentInputID:   agentInput.ID,
		},
		true,
	)
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	resolved, err := qtx.ResolveConfigChangeAgentInput(
		ctx,
		dbsqlc.ResolveConfigChangeAgentInputParams{
			ProjectID: input.ProjectID,
			AgentID:   input.AgentID,
			ID:        agentInput.ID,
			EventID:   eventRecord.Event.ID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentConfigChangeRecord{}, storeerr.ErrStateTransitionConflict
	}
	if err != nil {
		return AgentConfigChangeRecord{}, fmt.Errorf("resolve config-change agent input: %w", err)
	}
	agentInput.State = "resolved"
	agentInput.AdmittedEventID = eventRecord.Event.ID
	agentInput.AdmittedAt = resolved.AdmittedAt
	agentInput.ResolvedAt = resolved.ResolvedAt
	return AgentConfigChangeRecord{AgentInput: agentInput, Event: eventRecord.Event}, nil
}

func validateProjectPrincipalActionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	principal identitystore.PrincipalRecord,
	action string,
) error {
	if isNilID(projectID) {
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

func agentInputRecordFromConfigChangeSQLC(row dbsqlc.InsertConfigChangeAgentInputRow) AgentInputRecord {
	return AgentInputRecord{
		ID:                  row.ID,
		ProjectID:           row.ProjectID,
		AgentID:             row.AgentID,
		State:               row.State,
		InputRank:           row.InputRank,
		ActorID:             idFromSQLCPtr(row.ActorID),
		InputKind:           row.InputKind,
		IdempotencyScope:    row.IdempotencyScope,
		InputIdempotencyKey: row.InputIdempotencyKey,
		QueuedAt:            row.QueuedAt,
		AdmittedEventID:     idFromSQLCPtr(row.AdmittedEventID),
		AdmittedAt:          row.AdmittedAt,
		CanceledAt:          row.CanceledAt,
		DeliveryMode:        AgentInputDeliveryMode(row.DeliveryMode),
		ControlType:         row.ControlType,
		TargetInteractionID: idFromSQLCPtr(row.TargetInteractionID),
		AgentConfigID:       idFromSQLCPtr(row.AgentConfigID),
		ResolvedAt:          row.ResolvedAt,
		RejectedReason:      row.RejectedReason,
		Metadata:            row.Metadata,
	}
}
