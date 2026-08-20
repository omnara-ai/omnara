package executionstore

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const maxConnectBYOMachineProjectIDs = 100

type ConnectBYOMachineInput struct {
	OrgID       ID
	DisplayName string
	ProjectIDs  []ID
	TokenName   string
}

type ConnectBYOMachineResult struct {
	Machine       MachineRecord
	DaemonToken   CreatedMachineDaemonToken
	ProjectGrants []ProjectMachineGrantRecord
}

func (s *Store) ConnectBYOMachine(
	ctx context.Context,
	input ConnectBYOMachineInput,
) (ConnectBYOMachineResult, error) {
	if len(input.ProjectIDs) > maxConnectBYOMachineProjectIDs {
		return ConnectBYOMachineResult{}, storeerr.InvalidRequest(errors.New("too many project IDs"))
	}
	projectSet := make(map[ID]struct{}, len(input.ProjectIDs))
	for _, projectID := range input.ProjectIDs {
		if isNilID(projectID) {
			return ConnectBYOMachineResult{}, storeerr.InvalidRequest(errors.New("project ID is required"))
		}
		if _, exists := projectSet[projectID]; exists {
			return ConnectBYOMachineResult{}, storeerr.InvalidRequest(errors.New("project IDs must be unique"))
		}
		projectSet[projectID] = struct{}{}
	}
	machineInput, environment, machineMetadata, err := prepareDaemonMachineCreate(CreateDaemonMachineInput{
		OrgID:       input.OrgID,
		DisplayName: input.DisplayName,
	})
	if err != nil {
		return ConnectBYOMachineResult{}, storeerr.InvalidRequest(err)
	}
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ConnectBYOMachineResult{}, fmt.Errorf("begin connect BYO machine: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := dbsqlc.New(tx)
	if err := lifecyclelock.EnterActiveProjects(ctx, tx, input.OrgID, input.ProjectIDs); err != nil {
		return ConnectBYOMachineResult{}, err
	}
	machine, err := createDaemonMachineTx(ctx, qtx, machineInput, environment, machineMetadata)
	if err != nil {
		return ConnectBYOMachineResult{}, err
	}
	preparedToken, err := prepareBYOMachineDaemonTokenCreate(CreateBYOMachineDaemonTokenInput{
		OrgID:     input.OrgID,
		MachineID: machine.ID,
		Name:      input.TokenName,
	})
	if err != nil {
		return ConnectBYOMachineResult{}, err
	}
	tokenRecord, err := createBYOMachineDaemonTokenTx(ctx, qtx, preparedToken)
	if err != nil {
		return ConnectBYOMachineResult{}, err
	}
	grantMetadata, err := metadataColumn(nil, "project machine grant metadata")
	if err != nil {
		return ConnectBYOMachineResult{}, err
	}
	grants := make([]ProjectMachineGrantRecord, 0, len(input.ProjectIDs))
	for _, projectID := range input.ProjectIDs {
		grant, err := upsertExplicitProjectMachineGrantTx(
			ctx,
			qtx,
			CreateProjectMachineGrantInput{
				OrgID:     input.OrgID,
				ProjectID: projectID,
				MachineID: machine.ID,
			},
			grantMetadata,
		)
		if err != nil {
			return ConnectBYOMachineResult{}, fmt.Errorf("create project machine grant: %w", err)
		}
		grants = append(grants, grant)
	}
	if err := tx.Commit(ctx); err != nil {
		return ConnectBYOMachineResult{}, fmt.Errorf("commit connect BYO machine: %w", err)
	}
	return ConnectBYOMachineResult{
		Machine: machine,
		DaemonToken: CreatedMachineDaemonToken{
			Record: tokenRecord,
			Token:  preparedToken.token,
		},
		ProjectGrants: grants,
	}, nil
}
