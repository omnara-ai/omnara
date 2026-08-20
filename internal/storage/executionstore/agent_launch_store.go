package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type LaunchAgentInput struct {
	ProjectID     ID
	ProfileID     ID
	AgentConfigID ID
	LaunchedBy    identitystore.PrincipalRecord
	Name          string
	Message       string
	// MessageActor attributes the initial Message input. When nil, the actor
	// is derived from LaunchedBy, which must then be a user or org API key
	// principal.
	MessageActor   *ActorParams
	IdempotencyKey string
}

type LaunchAgentResult struct {
	Agent               AgentRecord
	AgentConfig         AgentConfigRecord
	ConfigChange        AgentConfigChangeRecord
	MCPServers          []agentconfig.RuntimeMCPServer
	MCPConnections      []MCPConnectionRecord
	MachineBindings     []AgentMachineBindingRecord
	ProvisionMachineIDs []ID
	AgentInput          AgentInputRecord
	InputContentBlocks  json.RawMessage
	Created             bool
}

func (s *Store) LaunchAgent(
	ctx context.Context,
	input LaunchAgentInput,
) (LaunchAgentResult, error) {
	if isNilID(input.ProjectID) || isNilID(input.AgentConfigID) || isNilID(input.LaunchedBy.ID) {
		return LaunchAgentResult{}, errors.New(
			"project, agent config, and launching principal are required",
		)
	}
	return storeutil.RetryTransaction(ctx, func() (LaunchAgentResult, error) {
		return s.launchAgentOnce(ctx, input)
	})
}

func (s *Store) launchAgentOnce(
	ctx context.Context,
	input LaunchAgentInput,
) (LaunchAgentResult, error) {
	project, err := loadProjectTx(ctx, s.q, input.ProjectID)
	if err != nil {
		return LaunchAgentResult{}, err
	}
	txNotifications := s.newTxNotifications()
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return LaunchAgentResult{}, fmt.Errorf("begin launch agent: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := s.q.WithTx(tx)
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
		if err != nil {
			return LaunchAgentResult{}, err
		}
		if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "idempotent launch agent"); err != nil {
			return LaunchAgentResult{}, err
		}
		return result, nil
	}
	var profile *AgentProfileRecord
	if input.ProfileID != NilID {
		record, err := lockAgentProfileTx(ctx, qtx, input.ProjectID, input.ProfileID)
		if err != nil {
			return LaunchAgentResult{}, err
		}
		profile = &record
	}
	config, contract, err := launchConfigTx(ctx, qtx, input.ProjectID, profile, input.AgentConfigID)
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
	agent, inserted, err := insertAdmittedAgentTx(ctx, tx, qtx, insertAgentInput{
		OrgID:           project.OrgID,
		ProjectID:       input.ProjectID,
		AgentProfileID:  input.ProfileID,
		Name:            launchAgentName(input.Name, profile),
		CurrentConfigID: config.ID,
		IdempotencyKey:  input.IdempotencyKey,
	})
	if err != nil {
		return LaunchAgentResult{}, err
	}
	if !inserted {
		if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "idempotent launch agent"); err != nil {
			return LaunchAgentResult{}, err
		}
		return LaunchAgentResult{Agent: agent}, nil
	}
	result := LaunchAgentResult{
		Agent:       agent,
		AgentConfig: config,
		MCPServers:  contract.MCPServers,
		Created:     true,
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
	for _, source := range machineSources {
		if source.PoolGrantForLaunch.ID == NilID {
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
	machineRefs, err := newMachineRefs(len(bindingRequests))
	if err != nil {
		return LaunchAgentResult{}, err
	}
	result.MachineBindings = make([]AgentMachineBindingRecord, 0, len(bindingRequests))
	for index, bindingRequest := range bindingRequests {
		source := bindingRequest.Source
		machineRef := machineRefs[index]
		switch {
		case source.GrantID != NilID:
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
				MachineRef:            machineRef,
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
		case source.PoolGrantForLaunch.ID != NilID:
			binding, err := allocateNewPoolMachineForAgentTx(
				ctx,
				qtx,
				project.OrgID,
				input.ProjectID,
				agent.ID,
				bindingRequest,
				machineRef,
			)
			if err != nil {
				return LaunchAgentResult{}, err
			}
			result.MachineBindings = append(result.MachineBindings, binding)
			result.ProvisionMachineIDs = append(result.ProvisionMachineIDs, binding.MachineID)
		}
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
	if err := s.commitTxWithNotifications(ctx, tx, txNotifications, "launch agent"); err != nil {
		return LaunchAgentResult{}, err
	}
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
	projectID ID,
	profile *AgentProfileRecord,
	configID ID,
) (AgentConfigRecord, agentconfig.RuntimeContract, error) {
	if configID == NilID {
		return AgentConfigRecord{}, agentconfig.RuntimeContract{}, errors.New(
			"agent config is required",
		)
	}
	if profile != nil && configID != profile.CurrentConfigID {
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

func launchAgentName(name string, profile *AgentProfileRecord) string {
	if name != "" {
		return name
	}
	if profile != nil {
		return profile.Name
	}
	return ""
}

func createAgentMCPConnectionsTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID ID,
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
