//go:build integration

package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type ModelWorkSeed struct {
	Kind                 ModelWorkKind
	ModelCallContextID   uuid.UUID
	SourceModelOutputID  uuid.UUID
	TurnID               uuid.UUID
	InputIDs             []uuid.UUID
	OpeningEventSequence int64
}

func (s *Store) IntegrationCommitTxWithNotifications(
	ctx context.Context,
	tx pgx.Tx,
	txNotifications *notifications.TxNotifications,
	operation string,
) error {
	return s.commitTxWithNotifications(ctx, tx, txNotifications, operation)
}

func (s *Store) AcquireAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, workerProcessID uuid.UUID,
	leaseDuration time.Duration,
) (AgentRuntimeLockRecord, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil || workerProcessID == uuid.Nil {
		return AgentRuntimeLockRecord{}, errors.New("project, agent, and worker process ids are required")
	}
	if err := validateAgentRuntimeLockLeaseDuration(leaseDuration); err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return AgentRuntimeLockRecord{}, fmt.Errorf("begin acquire agent runtime lock: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	record, _, err := acquireAgentRuntimeLockTx(
		ctx,
		dbsqlc.New(tx),
		projectID,
		agentID,
		workerProcessID,
		leaseDuration,
	)
	if err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AgentRuntimeLockRecord{}, fmt.Errorf("commit acquire agent runtime lock: %w", err)
	}
	return record, nil
}

func (s *Store) MarkAgentWakeup(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	metadata []byte,
) error {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return errors.New("project id and agent id are required")
	}
	if metadata == nil {
		metadata = []byte(`{}`)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin mark agent wakeup: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if _, err := qtx.LockAgentInProject(
		ctx,
		dbsqlc.LockAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		return fmt.Errorf("lock agent for wakeup: %w", err)
	}
	if err := qtx.MarkAgentWakeup(
		ctx,
		dbsqlc.MarkAgentWakeupParams{
			ProjectID: projectID,
			AgentID:   agentID,
			Metadata:  metadata,
		},
	); err != nil {
		return fmt.Errorf("mark agent wakeup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit mark agent wakeup: %w", err)
	}
	return nil
}

func (s *Store) DeleteAgentWakeup(ctx context.Context, projectID, agentID uuid.UUID) error {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return errors.New("project id and agent id are required")
	}
	if _, err := s.q.DeleteAgentWakeup(
		ctx,
		dbsqlc.DeleteAgentWakeupParams{ProjectID: projectID, AgentID: agentID},
	); err != nil {
		return fmt.Errorf("delete agent wakeup: %w", err)
	}
	return nil
}

func (s *Store) NextAgentModelWork(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (ModelWorkSeed, bool, error) {
	if projectID == uuid.Nil || agentID == uuid.Nil {
		return ModelWorkSeed{}, false, errors.New("project id and agent id are required")
	}
	row, err := s.q.NextAgentModelWork(
		ctx,
		dbsqlc.NextAgentModelWorkParams{ProjectID: projectID, AgentID: agentID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ModelWorkSeed{}, false, nil
	}
	if err != nil {
		return ModelWorkSeed{}, false, fmt.Errorf("next agent model work: %w", err)
	}
	return ModelWorkSeed{
		Kind:                 ModelWorkKind(row.WorkKind),
		ModelCallContextID:   row.ModelCallContextID,
		SourceModelOutputID:  row.ModelOutputID,
		TurnID:               row.TurnID,
		InputIDs:             row.InputIds,
		OpeningEventSequence: row.OpeningEventSequence,
	}, true, nil
}

func (s *Store) GetToolCallResultAuthorityByToolCall(
	ctx context.Context,
	projectID, agentID, toolCallID uuid.UUID,
) (ToolCallResultAuthorityRecord, bool, error) {
	return getToolCallResultAuthorityByToolCallTx(ctx, s.pool, projectID, agentID, toolCallID)
}

func (s *Store) RegisterDaemonRuntime(
	ctx context.Context,
	input RegisterDaemonRuntimeInput,
) (DaemonRuntimeRecord, error) {
	record, err := s.RegisterDaemonRuntimeWithReconciliation(ctx, input)
	if err != nil {
		return DaemonRuntimeRecord{}, err
	}
	return record.Runtime, nil
}

func (s *Store) ListCompletedToolCallsForTurn(
	ctx context.Context,
	projectID, agentID, turnID uuid.UUID,
) ([]ToolCallRecord, error) {
	watermark, err := s.MaxEventSequence(ctx, projectID, agentID)
	if err != nil {
		return nil, err
	}
	records, err := s.ListCompletedToolCallsAtWatermark(ctx, projectID, agentID, 0, watermark)
	if err != nil {
		return nil, err
	}
	filtered := make([]ToolCallRecord, 0, len(records))
	for _, record := range records {
		if record.TurnID == turnID {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

type IntegrationAdmitAgentInputAndOpenTurnInput struct {
	ProjectID uuid.UUID
	AgentID   uuid.UUID
}

type IntegrationCreateAgentContentInputTxResult struct {
	AgentInput             AgentInputRecord
	ContentBlocks          json.RawMessage
	Created                bool
	CanceledInteractionIDs []uuid.UUID
}

type IntegrationInsertAgentMachineBindingInput struct {
	ProjectID              uuid.UUID
	AgentID                uuid.UUID
	CreateToolCallID       uuid.UUID
	ProjectMachineGrantID  uuid.UUID
	BindingKind            AgentMachineBindingKind
	Description            string
	Cwd                    string
	EnvOverlay             json.RawMessage
	SecretEnvOverlay       json.RawMessage
	DeleteAfterIdleMinutes *int
	Metadata               json.RawMessage
}

type IntegrationLaunchMachineSource struct {
	Index              int
	Contract           agentconfig.RuntimeMachine
	GrantID            uuid.UUID
	PoolGrantForLaunch dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow
	Provisioning       MachineProvisioningConfig
	MachineCwd         string
	MachineEnvironment MachineEnvironment
	BindingConfig      MachineBindingConfig
}

type IntegrationPoolMachineBindingInput struct {
	OrgID            uuid.UUID
	ProjectID        uuid.UUID
	AgentID          uuid.UUID
	Description      string
	PoolGrant        dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow
	ResolvedMachine  ResolvedPoolMachine
	CreateToolCallID uuid.UUID
}

func IntegrationEnsureRuntimeLockActiveTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, runtimeID uuid.UUID,
) error {
	return ensureRuntimeLockActiveTx(ctx, tx, projectID, agentID, runtimeID)
}

func IntegrationSelectLockedSteeringAgentInputsForAdmissionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) ([]AgentInputRecord, error) {
	return selectLockedSteeringAgentInputsForAdmissionTx(ctx, qtx, projectID, agentID)
}

func IntegrationSelectLockedQueuedAgentInputForAdmissionTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) ([]AgentInputRecord, error) {
	return selectLockedQueuedAgentInputForAdmissionTx(ctx, qtx, projectID, agentID)
}

func IntegrationAdmitLockedAgentInputsAndOpenTurnTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input IntegrationAdmitAgentInputAndOpenTurnInput,
	lockedInputs []AgentInputRecord,
) (AdmittedAgentInputTurn, error) {
	return admitLockedAgentInputsAndOpenTurnTx(
		ctx,
		txNotifications,
		tx,
		qtx,
		admitAgentInputAndOpenTurnInput(input),
		lockedInputs,
	)
}

func IntegrationModelCallOpeningInputSet(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, turnID uuid.UUID,
	inputEventSequence int64,
) ([]uuid.UUID, int64, error) {
	return modelCallOpeningInputSet(ctx, qtx, projectID, agentID, turnID, inputEventSequence)
}

func IntegrationLoadAgentTx(ctx context.Context, tx pgx.Tx, id uuid.UUID) (AgentRecord, error) {
	return loadAgentTx(ctx, tx, id)
}

func IntegrationParseAgentInputContentBlocks(
	contentBlocks json.RawMessage,
) ([]CreateContentBlockInput, error) {
	return parseAgentInputContentBlocks(contentBlocks)
}

func IntegrationCreateAgentContentInputTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	agent AgentRecord,
	input CreateAgentContentInputInput,
	contentBlocks []CreateContentBlockInput,
) (IntegrationCreateAgentContentInputTxResult, error) {
	result, err := createAgentContentInputTx(ctx, txNotifications, tx, qtx, agent, input, contentBlocks)
	return IntegrationCreateAgentContentInputTxResult{
		AgentInput:             result.agentInput,
		ContentBlocks:          result.contentBlocks,
		Created:                result.created,
		CanceledInteractionIDs: result.canceledInteractionIDs,
	}, err
}

func IntegrationInsertAgentMachineBindingTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input IntegrationInsertAgentMachineBindingInput,
) (AgentMachineBindingRecord, error) {
	return insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput(input))
}

func IntegrationGetAgentMachineObservationByMachineID(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	machineID uuid.UUID,
) (AgentMachineObservationRecord, error) {
	return getAgentMachineObservationByMachineID(ctx, qtx, projectID, agentID, machineID)
}

func IntegrationListPoolMachinesTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
) ([]PoolMachineRecord, error) {
	return listPoolMachinesTx(ctx, qtx, projectID, agentID)
}

func IntegrationPoolMachineByIDTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	machineID uuid.UUID,
) (PoolMachineRecord, error) {
	return poolMachineByIDTx(ctx, qtx, projectID, agentID, machineID)
}

func IntegrationCreatePoolMachineBindingTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input IntegrationPoolMachineBindingInput,
) (AgentMachineBindingRecord, error) {
	return createPoolMachineBindingTx(ctx, qtx, poolMachineBindingInput(input))
}

func (s *Store) IntegrationResolveLaunchMachineSourcesTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	orgID, projectID uuid.UUID,
	sources []IntegrationLaunchMachineSource,
) error {
	ownerSources := make([]launchMachineSource, len(sources))
	for index := range sources {
		ownerSources[index] = launchMachineSource(sources[index])
	}
	if err := s.resolveLaunchMachineSourcesTx(ctx, tx, qtx, orgID, projectID, ownerSources); err != nil {
		return err
	}
	for index := range ownerSources {
		sources[index] = IntegrationLaunchMachineSource(ownerSources[index])
	}
	return nil
}

func IntegrationValidateResponseEnvelopeForModelCallContext(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	envelope modelenvelope.ResponseEnvelope,
	contextRow ModelCallContextRecord,
) error {
	return validateResponseEnvelopeForModelCallContext(ctx, qtx, envelope, contextRow)
}

func IntegrationRenewAgentRuntimeLockTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, runtimeLockID uuid.UUID,
	leaseDuration time.Duration,
) (AgentRuntimeLockRenewal, error) {
	return renewAgentRuntimeLockTx(ctx, qtx, projectID, agentID, runtimeLockID, leaseDuration)
}

func IntegrationReapExpiredAgentRuntimeLockTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	projectID, agentID, runtimeLockID uuid.UUID,
	retryBackoff func(int, string) time.Duration,
) (bool, error) {
	return reapExpiredAgentRuntimeLockTx(
		ctx,
		txNotifications,
		tx,
		projectID,
		agentID,
		runtimeLockID,
		retryBackoff,
	)
}

func IntegrationUpsertActorIdentityTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input UpsertActorIdentityInput,
) (ActorRecord, error) {
	return upsertActorIdentityTx(ctx, qtx, input)
}

func IntegrationCompactionSourceStartTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	contextRow ModelCallContextRecord,
) (int64, error) {
	return compactionSourceStartTx(ctx, qtx, contextRow)
}

func IntegrationCreateModelOutputAuthorityTx(
	ctx context.Context,
	db dbsqlc.DBTX,
	input CreateModelOutputAuthorityInput,
) (ModelOutputAuthorityRecord, error) {
	return createModelOutputAuthorityTx(ctx, db, input)
}

func IntegrationCreateContentBlockTx(
	ctx context.Context,
	db dbsqlc.DBTX,
	input CreateContentBlockInput,
) (ContentBlockRecord, error) {
	return createContentBlockTx(ctx, db, input)
}

func IntegrationAppendTypedAgentEventTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	input AppendTypedAgentEventInput,
) (TypedAgentEventRecord, error) {
	return appendTypedAgentEventTx(ctx, txNotifications, tx, input)
}

func IntegrationUpdateAgentTurnLatestEventQuery(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, turnID, latestEventID, latestSemanticEventID uuid.UUID,
) error {
	return updateAgentTurnLatestEventQuery(
		ctx,
		qtx,
		projectID,
		agentID,
		turnID,
		latestEventID,
		latestSemanticEventID,
	)
}

func IntegrationActivateAgentConfigTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) (AgentConfigChangeRecord, error) {
	config, err := loadAgentConfigTx(ctx, qtx, input.ProjectID, input.AgentConfigID)
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	if err := lockAgentConfigModelForUseTx(ctx, qtx, config); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	if err := lockAgentForConfigActivationTx(ctx, qtx, input); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	if err := authorizeAgentConfigChangeTx(ctx, qtx, input); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	return activateLockedAuthorizedAgentConfigTx(ctx, txNotifications, tx, qtx, input)
}

func (s *Store) IntegrationCompleteDaemonProcessAction(
	ctx context.Context,
	input CompleteDaemonProcessActionInput,
	state ProcessActionState,
) (DaemonProcessActionReportApplication, error) {
	return s.completeDaemonProcessAction(ctx, input, state)
}

func (s *Store) IntegrationCompleteMachineUnreachableToolCall(
	ctx context.Context,
	orgID, machineID uuid.UUID,
	fallbackAt time.Time,
	projectID, agentID, toolCallID uuid.UUID,
	result json.RawMessage,
	graceSeconds int32,
) (bool, error) {
	return s.completeMachineUnreachableToolCall(
		ctx,
		orgID,
		machineID,
		fallbackAt,
		projectID,
		agentID,
		toolCallID,
		result,
		graceSeconds,
	)
}

func IntegrationMachineStillUnreachableForToolExpiryTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	orgID, machineID uuid.UUID,
	fallbackAt time.Time,
	graceSeconds int32,
) (bool, error) {
	return machineStillUnreachableForToolExpiryTx(ctx, qtx, orgID, machineID, fallbackAt, graceSeconds)
}

func IntegrationAgentRuntimeLockRecordFromCancelSQLC(
	row dbsqlc.AgentRuntimeLock,
) AgentRuntimeLockRecord {
	return agentRuntimeLockRecordFromSQLC(row)
}

func IntegrationToolCallRecordFromInsertSQLC(row dbsqlc.InsertToolCallRow) ToolCallRecord {
	return toolCallRecordFromInsertSQLC(row)
}

func IntegrationAgentMachineBindingRecordFromSQLC(
	row dbsqlc.GetAgentMachineBindingByMachineRow,
) AgentMachineBindingRecord {
	return agentMachineBindingRecordFromSQLC(row)
}

func IntegrationResolveActorTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID uuid.UUID,
	params *ActorParams,
) (uuid.UUID, error) {
	return resolveActorTx(ctx, qtx, projectID, params)
}

func IntegrationGetToolCallTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, id uuid.UUID,
) (ToolCallRecord, error) {
	return getToolCallTx(ctx, tx, projectID, agentID, id)
}

func IntegrationAppendToolResultEventTx(
	ctx context.Context,
	txNotifications *notifications.TxNotifications,
	tx pgx.Tx,
	record ToolCallRecord,
) (events.Event, error) {
	return appendToolResultEventTx(ctx, txNotifications, tx, record)
}

func (s *Store) ArchiveIdleAgentsAsOf(
	ctx context.Context,
	asOf time.Time,
	limit int,
) ([]MachineRecord, int, error) {
	return s.archiveIdleAgents(ctx, &asOf, limit)
}

func (s *Store) ArchiveIdleAgentCandidateAsOf(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	asOf time.Time,
) (int, error) {
	_, archived, err := s.archiveIdleCandidates(
		ctx, []idleArchiveCandidate{{ProjectID: projectID, ID: agentID}}, &asOf,
	)
	return archived, err
}
