package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type PoolMachineRecord struct {
	Binding         AgentMachineBindingRecord `json:"binding"`
	Machine         MachineRecord             `json:"machine"`
	MachinePoolName string                    `json:"machine_pool_name"`
	FailureReport   json.RawMessage           `json:"failure_report,omitempty"`
}

type MachinePoolSourceRecord struct {
	MachinePoolID   ID     `json:"-"`
	MachinePoolName string `json:"machine_pool_name"`
	Description     string `json:"description,omitempty"`
}

type machineSource struct {
	Index         int
	Contract      agentconfig.RuntimeMachine
	MachineID     ID
	MachinePoolID ID
}

func decodeMachineSources(contract agentconfig.RuntimeContract) ([]machineSource, error) {
	if len(contract.MachineSources) == 0 {
		return nil, nil
	}
	out := make([]machineSource, 0, len(contract.MachineSources))
	for index, machine := range contract.MachineSources {
		source := machineSource{Index: index, Contract: machine}
		if err := validateRuntimeMachineSource(index, machine); err != nil {
			return nil, err
		}
		if machine.MachineID != "" {
			machineID, err := publicid.Decode(publicid.KindMachine, machine.MachineID)
			if err != nil {
				return nil, fmt.Errorf("machine_sources[%d].machine_id must be a machine public id: %w", index, err)
			}
			source.MachineID = machineID
		}
		if machine.MachinePoolID != "" {
			machinePoolID, err := publicid.Decode(publicid.KindMachinePool, machine.MachinePoolID)
			if err != nil {
				return nil, fmt.Errorf(
					"machine_sources[%d].machine_pool_id must be a machine pool public id: %w",
					index,
					err,
				)
			}
			source.MachinePoolID = machinePoolID
		}
		out = append(out, source)
	}
	return out, nil
}

type CreatePoolMachineInput struct {
	MachinePoolID ID
}

type CreatePoolMachineResult struct {
	Machine PoolMachineRecord
	Created bool
}

type DeletePoolMachineInput struct {
	MachineRef string
}

func (t *toolCallTransaction) createPoolMachine(
	ctx context.Context,
	input CreatePoolMachineInput,
) (CreatePoolMachineResult, error) {
	if isNilID(input.MachinePoolID) {
		return CreatePoolMachineResult{}, errors.New("machine pool is required")
	}
	projectID := t.input.ProjectID
	agentID := t.input.AgentID
	toolCallID := t.input.ToolCallID
	if replay, found, err := poolMachineByCreateToolCallTx(
		ctx,
		t.q,
		projectID,
		agentID,
		toolCallID,
	); err != nil {
		return CreatePoolMachineResult{}, err
	} else if found {
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return CreatePoolMachineResult{}, err
		}
		return CreatePoolMachineResult{Machine: replay, Created: false}, nil
	}
	if err := lifecyclelock.AgentSources(
		ctx,
		t.tx,
		[]ID{agentID},
	); err != nil {
		return CreatePoolMachineResult{}, err
	}
	if replay, found, err := poolMachineByCreateToolCallTx(
		ctx,
		t.q,
		projectID,
		agentID,
		toolCallID,
	); err != nil {
		return CreatePoolMachineResult{}, err
	} else if found {
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return CreatePoolMachineResult{}, err
		}
		return CreatePoolMachineResult{Machine: replay, Created: false}, nil
	}
	agent, err := loadAgentInProjectTx(ctx, t.tx, projectID, agentID)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	agentConfigID, err := t.q.GetToolCallAgentConfigID(
		ctx,
		dbsqlc.GetToolCallAgentConfigIDParams{
			ProjectID:  projectID,
			AgentID:    agentID,
			ToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return CreatePoolMachineResult{}, fmt.Errorf("tool call config context is unavailable: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return CreatePoolMachineResult{}, fmt.Errorf("load tool call config context: %w", err)
	}
	config, err := loadAgentConfigTx(ctx, t.q, projectID, agentConfigID)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	contract, err := launchableRuntimeContract(config)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	_, found, err := machineSourceForPool(contract, input.MachinePoolID)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	if !found {
		return CreatePoolMachineResult{}, fmt.Errorf(
			"machine pool is not configured for this agent: %w",
			storeerr.ErrNotFound,
		)
	}
	currentSource, err := currentAgentPoolMachineSourceTx(
		ctx,
		t.q,
		projectID,
		agent,
		input.MachinePoolID,
	)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	if err := lifecyclelock.Pools(ctx, t.tx, []lifecyclelock.PoolRef{{
		OrgID:  agent.OrgID,
		PoolID: currentSource.MachinePoolID,
	}}); err != nil {
		return CreatePoolMachineResult{}, err
	}
	poolGrant, err := t.q.GetActiveProjectMachinePoolGrantForLaunch(
		ctx,
		dbsqlc.GetActiveProjectMachinePoolGrantForLaunchParams{
			OrgID:         agent.OrgID,
			ProjectID:     projectID,
			MachinePoolID: currentSource.MachinePoolID,
		},
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return CreatePoolMachineResult{}, fmt.Errorf("machine pool is unavailable: %w", storeerr.ErrNotFound)
		}
		return CreatePoolMachineResult{}, fmt.Errorf("load agent machine pool grant: %w", err)
	}
	if err := t.lockForMutation(ctx); err != nil {
		return CreatePoolMachineResult{}, err
	}
	agent, err = loadAgentInProjectTx(ctx, t.tx, projectID, agentID)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	currentSource, err = currentAgentPoolMachineSourceTx(
		ctx,
		t.q,
		projectID,
		agent,
		input.MachinePoolID,
	)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	resolvedMachine, err := t.store.ResolvePoolMachineTx(
		ctx,
		t.q,
		poolGrant,
		currentSource.Contract,
	)
	if err != nil {
		return CreatePoolMachineResult{}, fmt.Errorf("machine source configuration: %w", err)
	}
	machineCount, err := poolMachineSourceStatusTx(ctx, t.q, projectID, agentID, currentSource)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	if machineCount >= currentSource.Contract.MaxMachines {
		return CreatePoolMachineResult{}, fmt.Errorf("machine pool limit reached: %w", storeerr.ErrStateTransitionConflict)
	}
	if err := ensurePoolCapacityForConfigTx(
		ctx,
		t.q,
		agent.OrgID,
		projectID,
		poolGrant,
		resolvedMachine.Provisioning,
		1,
	); err != nil {
		return CreatePoolMachineResult{}, err
	}
	machineRef, err := newMachineRef()
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	binding, err := createPoolMachineBindingTx(ctx, t.q, poolMachineBindingInput{
		OrgID:            agent.OrgID,
		ProjectID:        projectID,
		AgentID:          agentID,
		Description:      currentSource.Contract.Description,
		PoolGrant:        poolGrant,
		ResolvedMachine:  resolvedMachine,
		MachineRef:       machineRef,
		CreateToolCallID: toolCallID,
	})
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	status, err := poolMachineByRefTx(ctx, t.q, projectID, agentID, binding.MachineRef)
	if err != nil {
		return CreatePoolMachineResult{}, err
	}
	return CreatePoolMachineResult{Machine: status, Created: true}, nil
}

func (t *toolCallTransaction) deletePoolMachine(
	ctx context.Context,
	input DeletePoolMachineInput,
) (PoolMachineRecord, error) {
	if input.MachineRef == "" {
		return PoolMachineRecord{}, errors.New("machine ref is required")
	}
	projectID := t.input.ProjectID
	agentID := t.input.AgentID
	toolCallID := t.input.ToolCallID
	record, err := poolMachineByRefTx(ctx, t.q, projectID, agentID, input.MachineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, err
	}
	replay, err := validatePoolMachineDeletion(record, toolCallID)
	if err != nil {
		return PoolMachineRecord{}, err
	}
	if replay {
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return PoolMachineRecord{}, err
		}
		return record, nil
	}
	if err := lifecyclelock.AgentSources(
		ctx,
		t.tx,
		[]ID{agentID},
	); err != nil {
		return PoolMachineRecord{}, err
	}
	record, err = poolMachineByRefTx(ctx, t.q, projectID, agentID, input.MachineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, err
	}
	replay, err = validatePoolMachineDeletion(record, toolCallID)
	if err != nil {
		return PoolMachineRecord{}, err
	}
	if replay {
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return PoolMachineRecord{}, err
		}
		return record, nil
	}
	if err := lifecyclelock.Machines(
		ctx,
		t.tx,
		[]lifecyclelock.MachineRef{{OrgID: record.Machine.OrgID, MachineID: record.Machine.ID}},
	); err != nil {
		return PoolMachineRecord{}, err
	}
	record, err = poolMachineByRefTx(ctx, t.q, projectID, agentID, input.MachineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, err
	}
	replay, err = validatePoolMachineDeletion(record, toolCallID)
	if err != nil {
		return PoolMachineRecord{}, err
	}
	if replay {
		if err := t.lockOrAcceptExisting(ctx); err != nil {
			return PoolMachineRecord{}, err
		}
		return record, nil
	}
	if err := t.lockOrAcceptExisting(ctx); err != nil {
		return PoolMachineRecord{}, err
	}
	record, err = poolMachineByRefTx(ctx, t.q, projectID, agentID, input.MachineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, err
	}
	replay, err = validatePoolMachineDeletion(record, toolCallID)
	if err != nil {
		return PoolMachineRecord{}, err
	}
	if replay {
		return record, nil
	}
	if t.disposition != 0 {
		return PoolMachineRecord{}, storeerr.ErrIdempotencyConflict
	}
	binding, err := t.q.MarkAgentMachineBindingDeleteRequested(
		ctx,
		dbsqlc.MarkAgentMachineBindingDeleteRequestedParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			ID:               record.Binding.ID,
			DeleteToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, fmt.Errorf(
			"mark agent machine binding delete requested: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	if err != nil {
		return PoolMachineRecord{}, fmt.Errorf("mark agent machine binding delete requested: %w", err)
	}
	machineRow, err := t.q.MarkPoolMachineDeleting(ctx, dbsqlc.MarkPoolMachineDeletingParams{
		OrgID:                    record.Machine.OrgID,
		ID:                       record.Machine.ID,
		LifecycleReasonCode:      sqlcTextFromEmpty("machine_tool_delete"),
		LifecycleReasonMessage:   "deleted by machine tool",
		ExpectedLifecycleVersion: record.Machine.LifecycleVersion,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, fmt.Errorf("mark pool machine deleting: %w", storeerr.ErrStateTransitionConflict)
	}
	if err != nil {
		return PoolMachineRecord{}, fmt.Errorf("mark pool machine deleting: %w", err)
	}
	record.Binding = agentMachineBindingRecordFromSQLC(binding)
	record.Machine = machineRecordFromMarkPoolMachineDeletingSQLC(machineRow)
	return record, nil
}

func validatePoolMachineDeletion(record PoolMachineRecord, toolCallID ID) (bool, error) {
	if record.Binding.DeleteToolCallID != NilID {
		if record.Binding.DeleteToolCallID != toolCallID {
			return false, fmt.Errorf("machine deletion was already requested: %w", storeerr.ErrNotFound)
		}
		return true, nil
	}
	if record.Binding.State == AgentMachineBindingStateReleased || record.Machine.DeletedAt != nil ||
		record.Machine.LifecycleState == MachineLifecycleStateDeleting ||
		record.Machine.LifecycleState == MachineLifecycleStateDeleteFailed ||
		record.Machine.LifecycleState == MachineLifecycleStateDeleted {
		return false, fmt.Errorf("machine deletion was already requested: %w", storeerr.ErrNotFound)
	}
	if record.Machine.SourceKind != MachineSourceKindPool || record.Machine.MachinePoolID == NilID {
		return false, fmt.Errorf("machine is not pool-backed: %w", storeerr.ErrStateTransitionConflict)
	}
	return false, nil
}

func (s *Store) ListMachinePoolSources(
	ctx context.Context,
	projectID, agentID, agentConfigID ID,
) ([]MachinePoolSourceRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(agentConfigID) {
		return nil, errors.New("project, agent, and agent config are required")
	}
	return listMachinePoolSources(ctx, s.q, projectID, agentID, agentConfigID)
}

func (r *ToolCallReader) ListMachinePoolSources(
	ctx context.Context,
	agentConfigID ID,
) ([]MachinePoolSourceRecord, error) {
	if isNilID(agentConfigID) {
		return nil, errors.New("agent config is required")
	}
	t := r.transaction
	return listMachinePoolSources(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
		agentConfigID,
	)
}

func listMachinePoolSources(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID, agentConfigID ID,
) ([]MachinePoolSourceRecord, error) {
	if _, err := q.GetAgentInProject(
		ctx,
		dbsqlc.GetAgentInProjectParams{ProjectID: projectID, ID: agentID},
	); err != nil {
		return nil, fmt.Errorf("load agent in project: %w", err)
	}
	config, err := loadAgentConfigTx(ctx, q, projectID, agentConfigID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("agent config not found: %w", storeerr.ErrNotFound)
	}
	if err != nil {
		return nil, err
	}
	contract, err := launchableRuntimeContract(config)
	if err != nil {
		return nil, err
	}
	sources, err := decodeMachineSources(contract)
	if err != nil {
		return nil, err
	}
	configSource, err := agentconfig.ParseSource(
		agentconfig.SourceFormat(config.SourceFormat),
		[]byte(config.Source),
	)
	if err != nil {
		return nil, err
	}
	if len(configSource.MachineSources) != len(sources) {
		return nil, errors.New("agent config source does not match compiled machine sources")
	}
	out := make([]MachinePoolSourceRecord, 0, len(sources))
	for _, source := range sources {
		if source.MachinePoolID == NilID {
			continue
		}
		_, err := q.GetActiveProjectMachinePoolGrantForMachinePool(
			ctx,
			dbsqlc.GetActiveProjectMachinePoolGrantForMachinePoolParams{
				ProjectID:     projectID,
				MachinePoolID: source.MachinePoolID,
			},
		)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, MachinePoolSourceRecord{
			MachinePoolID:   source.MachinePoolID,
			MachinePoolName: strings.TrimSpace(configSource.MachineSources[source.Index].MachinePoolName),
			Description:     source.Contract.Description,
		})
	}
	return out, nil
}

func (r *ToolCallReader) ListPoolMachines(ctx context.Context) ([]PoolMachineRecord, error) {
	t := r.transaction
	return listPoolMachinesTx(ctx, t.q, t.input.ProjectID, t.input.AgentID)
}

func (s *Store) ListPoolMachines(
	ctx context.Context,
	projectID, agentID ID,
) ([]PoolMachineRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return nil, errors.New("project and agent are required")
	}
	return listPoolMachinesTx(ctx, s.q, projectID, agentID)
}

func listPoolMachinesTx(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
) ([]PoolMachineRecord, error) {
	rows, err := q.SelectPoolMachines(
		ctx,
		dbsqlc.SelectPoolMachinesParams{
			ProjectID:   projectID,
			AgentID:     agentID,
			BindingKind: string(MachineBindingKindPool),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("list pool machines: %w", err)
	}
	out := make([]PoolMachineRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, poolMachineRecordFromSQLC(row))
	}
	return out, nil
}

func (r *ToolCallReader) GetPoolMachineByRef(
	ctx context.Context,
	machineRef string,
) (PoolMachineRecord, error) {
	if machineRef == "" {
		return PoolMachineRecord{}, errors.New("machine ref is required")
	}
	t := r.transaction
	return getPoolMachineByRef(
		ctx,
		t.q,
		t.input.ProjectID,
		t.input.AgentID,
		machineRef,
	)
}

func (s *Store) GetPoolMachineByRef(
	ctx context.Context,
	projectID, agentID ID,
	machineRef string,
) (PoolMachineRecord, error) {
	if isNilID(projectID) || isNilID(agentID) {
		return PoolMachineRecord{}, errors.New("project and agent are required")
	}
	if machineRef == "" {
		return PoolMachineRecord{}, errors.New("machine ref is required")
	}
	return getPoolMachineByRef(ctx, s.q, projectID, agentID, machineRef)
}

func getPoolMachineByRef(
	ctx context.Context,
	q *dbsqlc.Queries,
	projectID, agentID ID,
	machineRef string,
) (PoolMachineRecord, error) {
	record, err := poolMachineByRefTx(ctx, q, projectID, agentID, machineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, fmt.Errorf("get pool machine: %w", err)
	}
	return record, nil
}

func poolMachineByCreateToolCallTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, toolCallID ID,
) (PoolMachineRecord, bool, error) {
	binding, err := qtx.GetAgentMachineBindingByCreateToolCall(
		ctx,
		dbsqlc.GetAgentMachineBindingByCreateToolCallParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			CreateToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, false, nil
	}
	if err != nil {
		return PoolMachineRecord{}, false, fmt.Errorf("load machine generated binding: %w", err)
	}
	record, err := poolMachineByRefTx(ctx, qtx, projectID, agentID, binding.MachineRef)
	if err != nil {
		return PoolMachineRecord{}, false, err
	}
	return record, true, nil
}

func (s *Store) GetPoolMachineByCreateToolCall(
	ctx context.Context,
	projectID, agentID, toolCallID ID,
) (PoolMachineRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(toolCallID) {
		return PoolMachineRecord{}, errors.New("project, agent, and tool call are required")
	}
	record, found, err := poolMachineByCreateToolCallTx(ctx, s.q, projectID, agentID, toolCallID)
	if err != nil {
		return PoolMachineRecord{}, err
	}
	if !found {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	return record, nil
}

func (s *Store) GetPoolMachineByDeleteToolCall(
	ctx context.Context,
	projectID, agentID, toolCallID ID,
) (PoolMachineRecord, error) {
	if isNilID(projectID) || isNilID(agentID) || isNilID(toolCallID) {
		return PoolMachineRecord{}, errors.New("project, agent, and tool call are required")
	}
	binding, err := s.q.GetAgentMachineBindingByDeleteToolCall(
		ctx,
		dbsqlc.GetAgentMachineBindingByDeleteToolCallParams{
			ProjectID:        projectID,
			AgentID:          agentID,
			DeleteToolCallID: toolCallID,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, fmt.Errorf("load machine deletion binding: %w", err)
	}
	record, err := poolMachineByRefTx(ctx, s.q, projectID, agentID, binding.MachineRef)
	if errors.Is(err, pgx.ErrNoRows) {
		return PoolMachineRecord{}, storeerr.ErrNotFound
	}
	if err != nil {
		return PoolMachineRecord{}, err
	}
	return record, nil
}

func poolMachineByRefTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
	machineRef string,
) (PoolMachineRecord, error) {
	rows, err := qtx.SelectPoolMachines(
		ctx,
		dbsqlc.SelectPoolMachinesParams{
			ProjectID:       projectID,
			AgentID:         agentID,
			BindingKind:     string(MachineBindingKindPool),
			MachineRef:      &machineRef,
			IncludeReleased: true,
		},
	)
	if err != nil {
		return PoolMachineRecord{}, err
	}
	if len(rows) == 0 {
		return PoolMachineRecord{}, pgx.ErrNoRows
	}
	return poolMachineRecordFromSQLC(rows[0]), nil
}

func currentAgentPoolMachineSourceTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID ID,
	agent AgentRecord,
	machinePoolID ID,
) (machineSource, error) {
	currentConfig, err := loadAgentConfigTx(ctx, qtx, projectID, agent.CurrentConfigID)
	if err != nil {
		return machineSource{}, err
	}
	currentContract, err := launchableRuntimeContract(currentConfig)
	if err != nil {
		return machineSource{}, err
	}
	currentSource, found, err := machineSourceForPool(currentContract, machinePoolID)
	if err != nil {
		return machineSource{}, err
	}
	if !found {
		return machineSource{}, fmt.Errorf(
			"machine pool is no longer configured for this agent: %w",
			storeerr.ErrStateTransitionConflict,
		)
	}
	return currentSource, nil
}

func machineSourceForPool(contract agentconfig.RuntimeContract, machinePoolID ID) (machineSource, bool, error) {
	sources, err := decodeMachineSources(contract)
	if err != nil {
		return machineSource{}, false, err
	}
	for _, source := range sources {
		if source.MachinePoolID != machinePoolID {
			continue
		}
		return source, true, nil
	}
	return machineSource{}, false, nil
}

func poolMachineSourceStatusTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
	source machineSource,
) (int, error) {
	count, err := qtx.CountActiveAgentPoolMachines(
		ctx,
		dbsqlc.CountActiveAgentPoolMachinesParams{
			ProjectID:     projectID,
			AgentID:       agentID,
			MachinePoolID: source.MachinePoolID,
		},
	)
	if err != nil {
		return 0, fmt.Errorf("count pool machines: %w", err)
	}
	return int(count), nil
}

type poolMachineBindingInput struct {
	OrgID            ID
	ProjectID        ID
	AgentID          ID
	Description      string
	PoolGrant        dbsqlc.GetActiveProjectMachinePoolGrantForLaunchRow
	ResolvedMachine  ResolvedPoolMachine
	MachineRef       string
	CreateToolCallID ID
}

func createPoolMachineBindingTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input poolMachineBindingInput,
) (AgentMachineBindingRecord, error) {
	poolGrant := input.PoolGrant
	if poolGrant.ID == NilID {
		return AgentMachineBindingRecord{}, errors.New("machine pool grant was not resolved")
	}
	provisioningColumns, err := machineProvisioningToColumns(input.ResolvedMachine.Provisioning)
	if err != nil {
		return AgentMachineBindingRecord{}, fmt.Errorf("prepare resolved machine columns: %w", err)
	}
	machineEnv, machineSecretEnv, err := machineEnvironmentToColumns(input.ResolvedMachine.MachineEnvironment)
	if err != nil {
		return AgentMachineBindingRecord{}, fmt.Errorf("prepare resolved machine environment: %w", err)
	}
	bindingEnvOverlay, bindingSecretEnvOverlay, err := MachineEnvironmentOverlayToColumns(
		input.ResolvedMachine.BindingConfig.EnvironmentOverlay,
	)
	if err != nil {
		return AgentMachineBindingRecord{}, fmt.Errorf("prepare resolved binding environment: %w", err)
	}
	machineRow, err := insertMachineWithResourceLimitTx(ctx, qtx, dbsqlc.InsertMachineParams{
		OrgID:                  input.OrgID,
		MachinePoolID:          &poolGrant.MachinePoolID,
		SourceKind:             string(MachineSourceKindPool),
		DisplayName:            "Instance of " + poolGrant.PoolName,
		Description:            input.Description,
		Provider:               poolGrant.Provider,
		LifecycleState:         string(MachineLifecycleStateProvisioning),
		Cpu:                    provisioningColumns.CPU,
		MemoryMb:               provisioningColumns.MemoryMB,
		Cwd:                    input.ResolvedMachine.MachineCwd,
		Env:                    machineEnv,
		SecretEnv:              machineSecretEnv,
		ProviderOptions:        &provisioningColumns.ProviderOptions,
		LifecycleReasonMessage: "",
		Metadata:               normalizedJSON(poolGrant.Metadata),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentMachineBindingRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentMachineBindingRecord{}, fmt.Errorf("insert pool machine: %w", err)
	}
	grantRow, err := qtx.UpsertProjectMachineGrant(ctx, dbsqlc.UpsertProjectMachineGrantParams{
		OrgID:                     input.OrgID,
		ProjectID:                 input.ProjectID,
		MachineID:                 machineRow.ID,
		SourceKind:                string(ProjectMachineGrantSourceKindPool),
		ProjectMachinePoolGrantID: &poolGrant.ID,
		Description:               poolGrant.Description,
		Metadata:                  json.RawMessage(`{}`),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AgentMachineBindingRecord{}, storeerr.ErrIdempotencyConflict
		}
		return AgentMachineBindingRecord{}, fmt.Errorf("insert generated project machine grant: %w", err)
	}
	return insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput{
		ProjectID:             input.ProjectID,
		AgentID:               input.AgentID,
		ProjectMachineGrantID: grantRow.ID,
		MachineRef:            input.MachineRef,
		BindingKind:           MachineBindingKindPool,
		Description:           input.Description,
		Cwd:                   input.ResolvedMachine.BindingConfig.Cwd,
		EnvOverlay:            bindingEnvOverlay,
		SecretEnvOverlay:      bindingSecretEnvOverlay,
		Metadata:              json.RawMessage(`{}`),
		CreateToolCallID:      input.CreateToolCallID,
	})
}

func poolMachineRecordFromSQLC(row dbsqlc.SelectPoolMachinesRow) PoolMachineRecord {
	binding := AgentMachineBindingRecord{
		ID:               row.ID,
		OrgID:            row.OrgID,
		ProjectID:        row.ProjectID,
		AgentID:          row.AgentID,
		CreateToolCallID: idFromSQLCPtr(row.CreateToolCallID),
		DeleteToolCallID: idFromSQLCPtr(row.DeleteToolCallID),
		MachineID:        row.MachineID,
		MachineRef:       row.MachineRef,
		BindingKind:      AgentMachineBindingKind(row.BindingKind),
		State:            AgentMachineBindingState(row.State),
		Description:      row.Description,
		Cwd:              row.Cwd,
		EnvOverlay:       row.EnvOverlay,
		SecretEnvOverlay: row.SecretEnvOverlay,
		Metadata:         row.Metadata,
		CreatedAt:        row.CreatedAt,
		UpdatedAt:        row.UpdatedAt,
	}
	machine := machineRecordFromSQLC(
		row.MachineID,
		row.OrgID,
		row.MachinePoolID,
		row.MachineSourceKind,
		row.MachineDisplayName,
		row.MachineDescription,
		row.Provider,
		row.LifecycleState,
		row.ProviderResourceID,
		row.ProviderProvisionAttemptedAt,
		row.ConnectionState,
		row.LastObservedAt,
		row.Cpu,
		row.MemoryMb,
		row.MachineCwd,
		row.MachineEnv,
		row.MachineSecretEnv,
		row.ProviderOptions,
		row.MachineIdempotencyKey,
		row.LifecycleReasonCode,
		row.LifecycleReasonMessage,
		row.NextReconcileAfter,
		row.ProvisionAttempts,
		row.DeleteAttempts,
		row.MachineMetadata,
		row.DeletedAt,
		row.MachineCreatedAt,
		row.MachineUpdatedAt,
		row.LifecycleChangedAt,
		row.LifecycleVersion,
	)
	return PoolMachineRecord{
		Binding:         binding,
		Machine:         machine,
		MachinePoolName: row.PoolName,
		FailureReport:   rawMessageFromSQLCPtr(row.FailureReport),
	}
}
