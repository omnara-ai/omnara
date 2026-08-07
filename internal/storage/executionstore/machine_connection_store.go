package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type ConnectBYOMachineInput struct {
	OrgID       ID
	DisplayName string
	ProjectIDs  []ID
	TokenName   string
	Token       string
}

type ConnectBYOMachineResult struct {
	Machine       MachineRecord
	TokenRecord   MachineDaemonTokenRecord
	ProjectGrants []ProjectMachineGrantRecord
}

func (s *Store) ConnectBYOMachine(
	ctx context.Context,
	input ConnectBYOMachineInput,
) (ConnectBYOMachineResult, error) {
	if len(input.ProjectIDs) > int(identitystore.MaxActiveProjectsPerOrg) {
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
	machineInput, environment, err := prepareDaemonMachineCreate(CreateDaemonMachineInput{
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
	projects, err := qtx.ListActiveProjectsForMachineConnection(
		ctx,
		dbsqlc.ListActiveProjectsForMachineConnectionParams{
			OrgID:      input.OrgID,
			ProjectIds: input.ProjectIDs,
		},
	)
	if err != nil {
		return ConnectBYOMachineResult{}, fmt.Errorf("list projects for machine connection: %w", err)
	}
	if len(projects) != len(input.ProjectIDs) {
		return ConnectBYOMachineResult{}, storeerr.ErrNotFound
	}
	machine, err := createDaemonMachineTx(ctx, qtx, machineInput, environment)
	if err != nil {
		return ConnectBYOMachineResult{}, err
	}
	tokenInput, err := prepareBYOMachineDaemonTokenCreate(CreateBYOMachineDaemonTokenInput{
		OrgID:     input.OrgID,
		MachineID: machine.ID,
		Name:      input.TokenName,
		Token:     input.Token,
		Metadata:  json.RawMessage(`{}`),
	})
	if err != nil {
		return ConnectBYOMachineResult{}, err
	}
	tokenRecord, err := createBYOMachineDaemonTokenTx(ctx, qtx, tokenInput)
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
				Metadata:  json.RawMessage(`{}`),
			},
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
		Machine:       machine,
		TokenRecord:   tokenRecord,
		ProjectGrants: grants,
	}, nil
}
