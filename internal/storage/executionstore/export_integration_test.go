//go:build integration

package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ModelWorkSeed struct {
	Kind                 ModelWorkKind
	ModelCallContextID   uuid.UUID
	SourceModelOutputID  uuid.UUID
	TurnID               uuid.UUID
	InputIDs             []uuid.UUID
	OpeningEventSequence int64
}

func (s *Store) IntegrationBeginUnit(ctx context.Context) (*agentexecution.Unit, error) {
	return s.cell.Begin(ctx)
}

func (s *Store) AcquireAgentRuntimeLock(
	ctx context.Context,
	projectID, agentID, workerID uuid.UUID,
	lease time.Duration,
) (AgentRuntimeLockRecord, error) {
	if err := validateAgentRuntimeLockLeaseDuration(lease); err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	unit, h, err := beginExecution(ctx, s, projectID, agentID)
	if err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	snapshot, err := h.LoadExecution(ctx)
	if err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	if snapshot.View.State != agentexecution.AgentActive {
		return AgentRuntimeLockRecord{}, storeerr.ErrAgentNotAdvanceable
	}
	var record AgentRuntimeLockRecord
	err = unit.DB().
		QueryRow(ctx,
			`INSERT INTO agent_runtime_locks(agent_id,worker_process_id,started_at,renewed_at,lease_expires_at)
 VALUES($1,$2,statement_timestamp(),statement_timestamp(),statement_timestamp()+$3::bigint*interval '1 microsecond')
 RETURNING id,agent_id,worker_process_id,started_at,renewed_at,lease_expires_at,cancel_requested_at`,
			agentID,
			workerID,
			lease.Microseconds()).
		Scan(&record.ID,
			&record.AgentID,
			&record.WorkerProcessID,
			&record.StartedAt,
			&record.RenewedAt,
			&record.LeaseExpiresAt,
			&record.CancelRequestedAt)
	if err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	if _, err = h.Repair(ctx); err != nil {
		return AgentRuntimeLockRecord{}, err
	}
	return record, unit.Commit(ctx, "seed runtime")
}

func (s *Store) RepairExecutionForTest(ctx context.Context, projectID, agentID uuid.UUID) error {
	unit, h, err := beginExecution(ctx, s, projectID, agentID)
	if err != nil {
		return err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	if _, err = h.Repair(ctx); err != nil {
		return err
	}
	return unit.Commit(ctx, "repair execution")
}

func (s *Store) DeleteAgentWakeup(ctx context.Context, projectID, agentID uuid.UUID) error {
	_, err := s.pool.Exec(
		ctx,
		`DELETE FROM agent_wakeups w USING agents a WHERE a.id=w.agent_id AND a.project_id=$1 AND a.id=$2`,
		projectID,
		agentID,
	)
	return err
}

func (s *Store) NextAgentModelWork(
	ctx context.Context,
	projectID, agentID uuid.UUID,
) (ModelWorkSeed, bool, error) {
	unit, h, err := beginExecution(ctx, s, projectID, agentID)
	if err != nil {
		return ModelWorkSeed{}, false, err
	}
	defer func() { _ = unit.Rollback(ctx) }()
	snapshot, err := h.LoadExecution(ctx)
	if err != nil {
		return ModelWorkSeed{}, false, err
	}
	work := snapshot.Selection.Model
	if work == nil {
		return ModelWorkSeed{}, false, nil
	}
	return ModelWorkSeed{
		Kind:                 ModelWorkKind(work.Kind),
		ModelCallContextID:   work.SourceContextID,
		SourceModelOutputID:  work.SourceOutputID,
		TurnID:               work.TurnID,
		InputIDs:             work.Opening.InputIDs,
		OpeningEventSequence: work.Opening.EventSequence,
	}, true, nil
}

func (s *Store) GetToolCallResultAuthorityByToolCall(
	ctx context.Context,
	projectID, agentID, toolID uuid.UUID,
) (ToolCallResultAuthorityRecord, bool, error) {
	row, err := s.q.GetToolCallResultByToolCall(
		ctx,
		dbsqlc.GetToolCallResultByToolCallParams{ProjectID: projectID, AgentID: agentID, ToolCallID: toolID},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ToolCallResultAuthorityRecord{}, false, nil
	}
	return toolCallResultAuthorityFromGetSQLC(row), err == nil, err
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
	unit *agentexecution.Unit,
	projectID, agentID, runtimeID uuid.UUID,
) error {
	if err := unit.LockAgentRefs(ctx,
		[]lifecyclelock.AgentRef{{ProjectID: projectID, AgentID: agentID}},
		agentexecution.RuntimeAuthority{AgentID: agentID, RuntimeLockID: runtimeID},
	); err != nil {
		return err
	}
	h, err := unit.Handle(projectID, agentID)
	if err != nil {
		return err
	}
	return h.FenceRuntime(ctx, runtimeID)
}

func IntegrationParseAgentInputContentBlocks(
	contentBlocks json.RawMessage,
) ([]CreateContentBlockInput, error) {
	return parseAgentInputContentBlocks(contentBlocks)
}

func IntegrationCreateAgentContentInputTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input CreateAgentContentInputInput,
	contentBlocks []CreateContentBlockInput,
) (IntegrationCreateAgentContentInputTxResult, error) {
	result, err := createAgentContentInputTx(ctx, unit, qtx, input, contentBlocks)
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
	db dbsqlc.DBTX,
	envelope modelenvelope.ResponseEnvelope,
	record ModelCallContextRecord,
) error {
	var slug string
	if err := db.QueryRow(ctx,
		`SELECT provider_model_slug FROM configured_model_revisions WHERE id=$1`,
		record.ConfiguredModelRevisionID).Scan(&slug); err != nil {
		return err
	}
	if slug != envelope.RequestedProviderModelSlug {
		return errors.New("requested model differs from captured revision")
	}
	return nil
}

func IntegrationRenewAgentRuntimeLockTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID, runtimeID uuid.UUID,
	lease time.Duration,
) (AgentRuntimeLockRenewal, error) {
	renewal, err := unit.RenewRuntime(
		ctx,
		agentexecution.AgentRoute{CellID: unit.CellID(), ProjectID: projectID, AgentID: agentID},
		runtimeID,
		lease,
	)
	if err != nil {
		return AgentRuntimeLockRenewal{}, err
	}
	row, err := dbsqlc.New(unit.DB()).
		GetAgentRuntimeLockForRelease(ctx,
			dbsqlc.GetAgentRuntimeLockForReleaseParams{ProjectID: projectID,
				AgentID: agentID,
				ID:      runtimeID})
	return AgentRuntimeLockRenewal{
		RuntimeLock:               agentRuntimeLockRecordFromSQLC(row),
		LocalLeaseBudgetStartedAt: renewal.LocalStartedAt,
	}, err
}

func IntegrationReapExpiredAgentRuntimeLockTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID, runtimeLockID uuid.UUID,
) (bool, error) {
	return reapExpiredAgentRuntimeLockTx(ctx, unit, projectID, agentID, runtimeLockID)
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
	db dbsqlc.DBTX,
	row ModelCallContextRecord,
) (int64, error) {
	var n int64
	err := db.QueryRow(ctx,
		`SELECT coalesce(max(summarized_through_event_sequence),0)+1 FROM context_checkpoints WHERE agent_id=$1 AND summarized_through_event_sequence<$2`,
		row.AgentID,
		row.SourceEventSequenceEnd).
		Scan(&n)
	return n, err
}

func IntegrationCreateModelOutputAuthorityTx(
	ctx context.Context,
	db dbsqlc.DBTX,
	input CreateModelOutputAuthorityInput,
) (ModelOutputAuthorityRecord, error) {
	var id uuid.UUID
	err := db.QueryRow(ctx,
		`INSERT INTO model_outputs(agent_id,model_call_context_id,served_provider_model_slug,stop_reason,provider_replay,created_at)
 SELECT c.agent_id,c.id,$4,$5,$6,statement_timestamp() FROM model_call_contexts c WHERE
 c.project_id=$1 AND c.agent_id=$2 AND c.id=$3
 RETURNING id`,
		input.ProjectID,
		input.AgentID,
		input.ModelCallContextID,
		input.ServedProviderModelSlug,
		input.StopReason,
		normalizedJSON(input.ProviderReplay)).
		Scan(&id)
	if err != nil {
		return ModelOutputAuthorityRecord{}, err
	}
	row, err := dbsqlc.New(db).
		GetModelOutputByModelContext(ctx,
			dbsqlc.GetModelOutputByModelContextParams{ProjectID: input.ProjectID,
				AgentID:            input.AgentID,
				ModelCallContextID: input.ModelCallContextID})
	return modelOutputAuthorityFromGetSQLC(row), err
}

func IntegrationCreateContentBlockTx(
	ctx context.Context,
	db dbsqlc.DBTX,
	input CreateContentBlockInput,
) (ContentBlockRecord, error) {
	metadata, err := input.Metadata.JSON()
	if err != nil {
		return ContentBlockRecord{}, err
	}
	var result ContentBlockRecord
	err = db.QueryRow(ctx,
		`INSERT INTO content_blocks(agent_id,owner_kind,owner_agent_input_id,owner_model_output_id,owner_tool_call_result_id,ordinal,block_kind,text_content,structured_data,artifact_id,tool_call_id,exclude_from_model_context,metadata,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,agent_payload_created_at($14,$1,$2,
 coalesce($3::uuid,$4::uuid,$5::uuid),false)) RETURNING id,created_at`,
		input.AgentID,
		input.OwnerKind,
		storeutil.IDFromNil(input.OwnerAgentInputID),
		storeutil.IDFromNil(input.OwnerModelOutputID),
		storeutil.IDFromNil(input.OwnerToolCallResultID),
		input.Ordinal,
		input.BlockKind,
		storeutil.TextFromEmpty(input.TextContent),
		sqlcRawMessageFromEmpty(input.StructuredData),
		storeutil.IDFromNil(input.ArtifactID),
		storeutil.IDFromNil(input.ToolCallID),
		input.ExcludeFromModelContext,
		metadata,
		input.ProjectID).
		Scan(&result.ID, &result.CreatedAt)
	return result, err
}

func IntegrationAppendTypedAgentEventTx(
	ctx context.Context,
	_ *notifications.TxNotifications,
	tx pgx.Tx,
	input AppendTypedAgentEventInput,
) (TypedAgentEventRecord, error) {
	id := input.ID
	if id == uuid.Nil {
		id = uuid.New()
	}
	result := TypedAgentEventRecord{
		TurnID:              input.TurnID,
		IsOpeningEvent:      input.IsOpeningEvent,
		AgentInputID:        input.AgentInputID,
		ModelOutputID:       input.ModelOutputID,
		ToolCallResultID:    input.ToolCallResultID,
		ContextCheckpointID: input.ContextCheckpointID,
		Event: events.Event{
			ID:             id,
			AgentID:        input.AgentID,
			Kind:           input.Kind,
			IdempotencyKey: input.IdempotencyKey,
		},
	}
	kind := string(input.Kind)
	if input.ToolCallResultID != uuid.Nil {
		kind = "tool_call_result"
	}
	err := tx.QueryRow(ctx,
		`WITH allocated AS (UPDATE agents SET next_event_sequence=next_event_sequence+1 WHERE id=$1 AND project_id=$2 RETURNING next_event_sequence-1 AS sequence)
 INSERT INTO agent_events(id,agent_id,turn_id,sequence,event_kind,agent_input_id,
 model_output_id,context_checkpoint_id,tool_call_result_id,is_opening_event,idempotency_key,created_at)
 SELECT $3,$1,$4,sequence,$5,$6,$7,$8,$9,$10,$11,agent_payload_created_at($2,$1,$12,
 coalesce($6::uuid,$7::uuid,$8::uuid,$9::uuid),true) FROM allocated RETURNING sequence,created_at`,
		input.AgentID,
		input.ProjectID,
		id,
		input.TurnID,
		input.Kind,
		storeutil.IDFromNil(input.AgentInputID),
		storeutil.IDFromNil(input.ModelOutputID),
		storeutil.IDFromNil(input.ContextCheckpointID),
		storeutil.IDFromNil(input.ToolCallResultID),
		input.IsOpeningEvent,
		storeutil.TextFromEmpty(input.IdempotencyKey),
		kind).
		Scan(&result.Event.Sequence, &result.Event.At)
	return result, err
}

func IntegrationUpdateAgentTurnLatestEventQuery(
	ctx context.Context,
	db dbsqlc.DBTX,
	projectID, agentID, turnID, latest, semantic uuid.UUID,
) error {
	_, err := db.Exec(
		ctx,
		`UPDATE agent_turns t SET latest_event_id=$4,latest_semantic_event_id=coalesce($5,latest_semantic_event_id) FROM agents a WHERE a.id=t.agent_id AND a.project_id=$1 AND t.agent_id=$2 AND t.id=$3`,
		projectID,
		agentID,
		turnID,
		latest,
		storeutil.IDFromNil(semantic),
	)
	return err
}

func IntegrationActivateAgentConfigTx(
	ctx context.Context,
	unit *agentexecution.Unit,
	qtx *dbsqlc.Queries,
	input ActivateAgentConfigInput,
) (AgentConfigChangeRecord, error) {
	config, err := loadAgentConfigTx(ctx, qtx, input.ProjectID, input.AgentConfigID)
	if err != nil {
		return AgentConfigChangeRecord{}, err
	}
	if err := lockAgentConfigForUseTx(ctx, qtx, config); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	if err := lockAgentForConfigActivationTx(ctx, unit, input); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	if err := authorizeAgentConfigChangeTx(ctx, qtx, input); err != nil {
		return AgentConfigChangeRecord{}, err
	}
	return activateLockedAuthorizedAgentConfigTx(ctx, unit, qtx, input)
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
	notifications *notifications.TxNotifications,
	tx pgx.Tx,
	record ToolCallRecord,
) (events.Event, error) {
	var resultID uuid.UUID
	err := tx.QueryRow(ctx,
		`INSERT INTO tool_call_results(agent_id,tool_call_id,outcome,completed_at) VALUES($1,$2,$3,statement_timestamp()) RETURNING id`,
		record.AgentID,
		record.ID,
		record.Outcome).
		Scan(&resultID)
	if err != nil {
		return events.Event{}, err
	}
	blocks, err := parseToolResultContentBlocks(record.ResultContentParts)
	if err != nil {
		return events.Event{}, err
	}
	for _, block := range blocks {
		block.ProjectID = record.ProjectID
		block.AgentID = record.AgentID
		block.OwnerKind = ContentBlockOwnerToolCallResult
		block.OwnerToolCallResultID = resultID
		if _, err = IntegrationCreateContentBlockTx(ctx, tx, block); err != nil {
			return events.Event{}, err
		}
	}
	event, err := IntegrationAppendTypedAgentEventTx(
		ctx,
		notifications,
		tx,
		AppendTypedAgentEventInput{
			ProjectID:        record.ProjectID,
			AgentID:          record.AgentID,
			TurnID:           record.TurnID,
			Kind:             events.KindToolResult,
			ToolCallResultID: resultID,
			IdempotencyKey:   "tool_result:" + record.ID.String(),
		},
	)
	if err != nil {
		return events.Event{}, err
	}
	err = IntegrationUpdateAgentTurnLatestEventQuery(
		ctx,
		tx,
		record.ProjectID,
		record.AgentID,
		record.TurnID,
		event.Event.ID,
		event.Event.ID,
	)
	return event.Event, err
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

func IntegrationAdmitInputs(
	ctx context.Context,
	unit *agentexecution.Unit,
	projectID, agentID uuid.UUID,
) (AdmittedAgentInputTurn, error) {
	h, err := unit.Handle(projectID, agentID)
	if err != nil {
		return AdmittedAgentInputTurn{}, err
	}
	admitted, err := h.AdmitInputs(ctx)
	if err != nil {
		return AdmittedAgentInputTurn{}, err
	}
	if err := applyAdmissionDestination(ctx, unit, projectID, agentID, admitted); err != nil {
		return AdmittedAgentInputTurn{}, err
	}
	return shapeAdmission(ctx, dbsqlc.New(unit.DB()), projectID, agentID, admitted)
}

type AppendTypedAgentEventInput struct {
	ID                  uuid.UUID
	ProjectID           uuid.UUID
	AgentID             uuid.UUID
	TurnID              uuid.UUID
	IsOpeningEvent      bool
	Kind                events.Kind
	IdempotencyKey      string
	AgentInputID        uuid.UUID
	ModelOutputID       uuid.UUID
	ToolCallResultID    uuid.UUID
	ContextCheckpointID uuid.UUID
}

type ContentBlockRecord struct {
	ID                    uuid.UUID
	ProjectID             uuid.UUID
	AgentID               uuid.UUID
	OwnerKind             ContentBlockOwnerKind
	OwnerAgentInputID     uuid.UUID
	OwnerModelOutputID    uuid.UUID
	OwnerToolCallResultID uuid.UUID
	Ordinal               int32
	BlockKind             ContentBlockKind
	TextContent           string
	StructuredData        json.RawMessage
	ArtifactID            uuid.UUID
	ToolCallID            uuid.UUID
	CreatedAt             time.Time
}

func sqlcRawMessageFromEmpty(value json.RawMessage) *json.RawMessage {
	if len(value) == 0 {
		return nil
	}
	return &value
}

func toolCallResultAuthorityFromGetSQLC(
	row dbsqlc.GetToolCallResultByToolCallRow,
) ToolCallResultAuthorityRecord {
	return ToolCallResultAuthorityRecord{
		ID:          row.ID,
		ProjectID:   row.ProjectID,
		AgentID:     row.AgentID,
		TurnID:      row.TurnID,
		ToolCallID:  row.ToolCallID,
		Outcome:     ToolResultOutcome(row.Outcome),
		CompletedAt: row.CompletedAt,
	}
}

type ToolCallResultAuthorityRecord struct {
	ID          uuid.UUID
	ProjectID   uuid.UUID
	AgentID     uuid.UUID
	TurnID      uuid.UUID
	ToolCallID  uuid.UUID
	Outcome     ToolResultOutcome
	CompletedAt time.Time
}
