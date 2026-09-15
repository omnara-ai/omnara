package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type LaunchAgentInput struct {
	ProjectID     uuid.UUID
	ProfileID     uuid.UUID
	AgentConfigID uuid.UUID
	LaunchedBy    identitystore.PrincipalRecord
	Name          *string
	Message       string
	// MessageActor attributes the initial Message input. When nil, the actor
	// is derived from LaunchedBy, which must then be a user or org API key
	// principal.
	MessageActor            *ActorParams
	IdempotencyKey          string
	ArchiveAfterIdleMinutes *int
	DerivedConfig           *CreateAgentConfigInput
	Subagent                *SubagentLaunch
}

type LaunchAgentResult struct {
	Agent               AgentRecord
	AgentConfig         AgentConfigRecord
	ConfigChange        AgentConfigChangeRecord
	MCPServers          []agentconfig.RuntimeMCPServer
	MCPConnections      []MCPConnectionRecord
	MachineBindings     []AgentMachineBindingRecord
	ProvisionMachineIDs []uuid.UUID
	AgentInput          AgentInputRecord
	InputContentBlocks  json.RawMessage
	Created             bool
}

func (s *Store) LaunchAgent(
	ctx context.Context,
	input LaunchAgentInput,
) (LaunchAgentResult, error) {
	input, err := validateLaunchAgentInput(input)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	return storeutil.RetryTransaction(ctx, "launch_agent", func() (LaunchAgentResult, error) {
		return s.launchAgentOnce(ctx, input)
	})
}

func validateLaunchAgentInput(input LaunchAgentInput) (LaunchAgentInput, error) {
	if input.ProjectID == uuid.Nil || input.LaunchedBy.ID == uuid.Nil {
		return LaunchAgentInput{}, errors.New("project and launching principal are required")
	}
	if (input.AgentConfigID == uuid.Nil) == (input.DerivedConfig == nil) {
		return LaunchAgentInput{}, errors.New("exactly one of agent config or derived config is required")
	}
	if input.Name != nil {
		name, err := resourcename.CanonicalizeAllowEmpty("agent name", *input.Name)
		if err != nil {
			return LaunchAgentInput{}, storeerr.InvalidRequest(err)
		}
		input.Name = &name
	}
	return input, nil
}

func (s *Store) launchAgentOnce(
	ctx context.Context,
	input LaunchAgentInput,
) (LaunchAgentResult, error) {
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LaunchAgentResult{}, fmt.Errorf("begin launch agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := s.launchAgentTx(ctx, tx, s.q.WithTx(tx), txNotifications, input)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	scope := "launch agent"
	if !result.Created {
		scope = "idempotent launch agent"
	}
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, scope); err != nil {
		return LaunchAgentResult{}, err
	}
	return result, nil
}

func (s *Store) launchAgentTx(
	ctx context.Context,
	tx pgx.Tx,
	qtx *dbsqlc.Queries,
	txNotifications *notifications.TxNotifications,
	input LaunchAgentInput,
) (LaunchAgentResult, error) {
	project, err := loadProjectTx(ctx, qtx, input.ProjectID)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if err := lifecyclelock.EnterActiveProject(ctx, tx, project.OrgID, input.ProjectID); err != nil {
		return LaunchAgentResult{}, err
	}
	if input.IdempotencyKey != "" {
		if err := qtx.LockAgentLaunchIdempotencyKey(ctx, dbsqlc.LockAgentLaunchIdempotencyKeyParams{
			ProjectID:      input.ProjectID,
			IdempotencyKey: input.IdempotencyKey,
		}); err != nil {
			return LaunchAgentResult{}, fmt.Errorf("lock agent launch idempotency key: %w", err)
		}
	}

	if result, found, err := launchReplayMaybeTx(ctx, qtx, input); err != nil || found {
		return result, err
	}
	if err := dbsafe.Text(input.Message); err != nil {
		return LaunchAgentResult{}, storeerr.InvalidRequest(fmt.Errorf("message %w", err))
	}
	var profile *AgentProfileRecord
	if input.ProfileID != uuid.Nil {
		record, err := lockAgentProfileTx(ctx, qtx, input.ProjectID, input.ProfileID)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		profile = &record
	}
	if input.Subagent != nil {
		if err := lockSubagentParentSourcesTx(ctx, tx, *input.Subagent); err != nil {
			return LaunchAgentResult{}, err
		}
	}
	agentName := launchAgentName(input.Name, profile)
	configID := input.AgentConfigID
	if input.DerivedConfig != nil {
		derived := *input.DerivedConfig
		derived.OrgID = project.OrgID
		derived.ProjectID = input.ProjectID
		created, err := insertAgentConfigTx(ctx, qtx, derived)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		configID = created.ID
	}
	config, contract, err := launchConfigTx(ctx, qtx, input.ProjectID, profile, configID, input.DerivedConfig != nil)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if err := lockAgentConfigModelForUseTx(ctx, qtx, config); err != nil {
		return LaunchAgentResult{}, err
	}
	machineSources, err := decodeLaunchMachineSources(contract)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	insertInput := insertAgentInput{
		OrgID:                   project.OrgID,
		ProjectID:               input.ProjectID,
		AgentProfileID:          input.ProfileID,
		Name:                    agentName,
		CurrentConfigID:         config.ID,
		IdempotencyKey:          input.IdempotencyKey,
		ArchiveAfterIdleMinutes: input.ArchiveAfterIdleMinutes,
	}
	var sharedBindings []dbsqlc.ListParentMachineBindingsForSharingRow
	if input.Subagent != nil {
		lockedMachineIDs, err := lockParentMachineBindingsForSharingTx(
			ctx, tx, qtx, project.OrgID, input.ProjectID, input.Subagent.ParentAgentID,
		)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		if err := admitSubagentLaunchTx(ctx, tx, qtx, input.ProjectID, *input.Subagent); err != nil {
			return LaunchAgentResult{}, err
		}
		sharedBindings, err = revalidateParentMachineBindingsForSharingTx(
			ctx, qtx, input.ProjectID, input.Subagent.ParentAgentID, lockedMachineIDs,
		)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		insertInput.ParentAgentID = input.Subagent.ParentAgentID
		insertInput.SubagentKey = input.Subagent.Key
		machineSources = nil
	}
	if err := s.resolveLaunchMachineSourcesTx(
		ctx,
		tx,
		qtx,
		project.OrgID,
		input.ProjectID,
		machineSources,
	); err != nil {
		return LaunchAgentResult{}, err
	}
	agent, inserted, err := insertAdmittedAgentTx(ctx, tx, qtx, insertInput)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if !inserted {
		return LaunchAgentResult{Agent: agent}, nil
	}
	result := LaunchAgentResult{
		Agent:       agent,
		AgentConfig: config,
		MCPServers:  contract.MCPServers,
		Created:     true,
	}
	for _, source := range machineSources {
		if source.PoolGrantForLaunch.ID == uuid.Nil {
			continue
		}
		if err := ensurePoolCapacityForConfigTx(
			ctx,
			qtx,
			project.OrgID,
			input.ProjectID,
			source.PoolGrantForLaunch,
			source.Provisioning,
			source.Contract.InitialNumMachines,
		); err != nil {
			return LaunchAgentResult{}, err
		}
	}
	configChange, err := activateNewAgentConfigTx(ctx, txNotifications, tx, qtx, ActivateAgentConfigInput{
		ProjectID:      input.ProjectID,
		AgentID:        agent.ID,
		AgentConfigID:  config.ID,
		ActorType:      input.LaunchedBy.Type,
		ActorID:        input.LaunchedBy.ID,
		Reason:         "launch",
		IdempotencyKey: "launch:" + agent.ID.String(),
	})
	if err != nil {
		return LaunchAgentResult{}, err
	}
	result.Agent = agent
	result.ConfigChange = configChange
	result.MCPConnections, err = createAgentMCPConnectionsTx(
		ctx,
		qtx,
		input.ProjectID,
		agent.ID,
		contract.MCPServers,
	)
	if err != nil {
		return LaunchAgentResult{}, err
	}

	bindingRequests, err := expandLaunchMachineBindingRequests(machineSources)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	result.MachineBindings = make([]AgentMachineBindingRecord, 0, len(bindingRequests))
	for _, bindingRequest := range bindingRequests {
		source := bindingRequest.Source
		switch {
		case source.GrantID != uuid.Nil:
			envOverlay, secretEnvOverlay, err := MachineEnvironmentOverlayToColumns(
				source.BindingConfig.EnvironmentOverlay,
			)
			if err != nil {
				return LaunchAgentResult{}, fmt.Errorf("prepare machine source environment: %w", err)
			}
			binding, err := insertAgentMachineBindingTx(ctx, qtx, insertAgentMachineBindingInput{
				ProjectID:             input.ProjectID,
				AgentID:               agent.ID,
				ProjectMachineGrantID: source.GrantID,
				BindingKind:           MachineBindingKindExplicit,
				Description:           source.Contract.Description,
				Cwd:                   source.BindingConfig.Cwd,
				EnvOverlay:            envOverlay,
				SecretEnvOverlay:      secretEnvOverlay,
				Metadata:              json.RawMessage(`{}`),
			})
			if err != nil {
				return LaunchAgentResult{}, err
			}
			result.MachineBindings = append(result.MachineBindings, binding)
		case source.PoolGrantForLaunch.ID != uuid.Nil:
			binding, err := allocateNewPoolMachineForAgentTx(
				ctx,
				qtx,
				project.OrgID,
				input.ProjectID,
				agent.ID,
				bindingRequest,
			)
			if err != nil {
				return LaunchAgentResult{}, err
			}
			result.MachineBindings = append(result.MachineBindings, binding)
			result.ProvisionMachineIDs = append(result.ProvisionMachineIDs, binding.MachineID)
		}
	}
	if input.Subagent != nil {
		shared, err := shareParentMachineBindingsTx(ctx, qtx, input.ProjectID, agent.ID, sharedBindings)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		result.MachineBindings = append(result.MachineBindings, shared...)
	}
	if input.Message != "" {
		agentInput, contentBlocks, err := insertLaunchInitialContentInputTx(
			ctx,
			tx,
			agent,
			input.LaunchedBy,
			input.MessageActor,
			input.Message,
			input.IdempotencyKey,
		)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		result.AgentInput = agentInput
		result.InputContentBlocks = contentBlocks
		if err := qtx.MarkAgentWakeup(
			ctx,
			dbsqlc.MarkAgentWakeupParams{
				ProjectID: input.ProjectID,
				AgentID:   agent.ID,
				Metadata:  []byte(`{"reason":"agent_input"}`),
			},
		); err != nil {
			return LaunchAgentResult{}, fmt.Errorf("mark launch agent wakeup: %w", err)
		}
	}
	txNotifications.AddAgentChange(input.ProjectID, agent.ID, notifications.AgentChangeAgent)
	return result, nil
}

func launchReplayMaybeTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	input LaunchAgentInput,
) (LaunchAgentResult, bool, error) {
	if input.IdempotencyKey == "" {
		return LaunchAgentResult{}, false, nil
	}
	row, err := qtx.GetAgentByIdempotencyKey(
		ctx,
		dbsqlc.GetAgentByIdempotencyKeyParams{
			ProjectID:      input.ProjectID,
			IdempotencyKey: input.IdempotencyKey,
		},
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return LaunchAgentResult{}, false, nil
	}
	if err != nil {
		return LaunchAgentResult{}, false, fmt.Errorf("load idempotent launch agent: %w", err)
	}
	agent := agentRecordFromIdempotencySQLC(row)
	return LaunchAgentResult{Agent: agent}, true, nil
}

func launchConfigTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID uuid.UUID,
	profile *AgentProfileRecord,
	configID uuid.UUID,
	derived bool,
) (AgentConfigRecord, agentconfig.RuntimeContract, error) {
	if configID == uuid.Nil {
		return AgentConfigRecord{}, agentconfig.RuntimeContract{}, errors.New(
			"agent config is required",
		)
	}
	if profile != nil && !derived && configID != profile.CurrentConfigID {
		matched, err := qtx.AgentProfileVersionExistsForConfig(
			ctx,
			dbsqlc.AgentProfileVersionExistsForConfigParams{
				ProjectID:     projectID,
				ProfileID:     profile.ID,
				AgentConfigID: configID,
			},
		)
		if err != nil {
			return AgentConfigRecord{}, agentconfig.RuntimeContract{}, fmt.Errorf(
				"check agent config belongs to agent profile: %w",
				err,
			)
		}
		if !matched {
			return AgentConfigRecord{}, agentconfig.RuntimeContract{}, fmt.Errorf(
				"agent config does not belong to agent profile %q: %w",
				profile.Name,
				storeerr.ErrNotFound,
			)
		}
	}
	config, err := loadAgentConfigTx(ctx, qtx, projectID, configID)
	if err != nil {
		return AgentConfigRecord{}, agentconfig.RuntimeContract{}, err
	}
	contract, err := launchableRuntimeContract(config)
	if err != nil {
		return AgentConfigRecord{}, agentconfig.RuntimeContract{}, err
	}
	return config, contract, nil
}

func launchAgentName(name *string, profile *AgentProfileRecord) string {
	if name != nil {
		return *name
	}
	if profile != nil {
		return profile.Name
	}
	return ""
}

func createAgentMCPConnectionsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID uuid.UUID,
	servers []agentconfig.RuntimeMCPServer,
) ([]MCPConnectionRecord, error) {
	if len(servers) == 0 {
		return nil, nil
	}
	out := make([]MCPConnectionRecord, 0, len(servers))
	for _, server := range servers {
		configHash, err := mcpServerConfigHash(server)
		if err != nil {
			return nil, fmt.Errorf("hash mcp server %q config: %w", server.ServerKey, err)
		}
		row, err := qtx.GetOrCreateMCPConnection(ctx, dbsqlc.GetOrCreateMCPConnectionParams{
			ProjectID:   projectID,
			AgentID:     agentID,
			ServerKey:   server.ServerKey,
			EndpointUrl: server.URL,
			ConfigHash:  configHash,
		})
		if err != nil {
			return nil, fmt.Errorf("create agent mcp connection %q: %w", server.ServerKey, err)
		}
		out = append(out, mcpConnectionRecordFromSQLC(row))
	}
	return out, nil
}

func launchableRuntimeContract(config AgentConfigRecord) (agentconfig.RuntimeContract, error) {
	if config.CompilerVersion != agentconfig.CompilerVersion {
		return agentconfig.RuntimeContract{}, fmt.Errorf(
			"agent config compiler contract %q is not launchable: %w",
			config.CompilerVersion,
			storeerr.ErrStateTransitionConflict,
		)
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		config.CompiledDefinition,
		config.CompilerVersion,
		config.EffectiveDefinitionHash,
	)
	if err != nil {
		return agentconfig.RuntimeContract{}, fmt.Errorf(
			"agent config runtime contract is not launchable: %w",
			errors.Join(storeerr.ErrStateTransitionConflict, err),
		)
	}
	return contract, nil
}
