package httpapi

import (
	"context"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

type machineDaemonScope struct {
	OrgID         storage.ID
	MachineID     storage.ID
	DaemonTokenID storage.ID
}

func (s *Server) orgScope(
	ctx context.Context,
	orgIDRaw string,
	action string,
) (identitystore.OrgRecord, *apierror.ResponseError) {
	if s.store == nil {
		err := apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return identitystore.OrgRecord{}, &err
	}
	orgID, err := parsePublicID(publicid.KindOrganization, orgIDRaw)
	if err != nil {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return identitystore.OrgRecord{}, &err
	}
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return identitystore.OrgRecord{}, &err
	}
	authInput := identitystore.AuthorizeOrgInput{
		Principal: principal,
		OrgID:     orgID,
		Action:    action,
	}
	allowed, err := s.store.Identity().AuthorizeOrg(ctx, authInput)
	if err != nil {
		logent.AuthorizationCheckFailed(ctx, err)
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return identitystore.OrgRecord{}, &err
	}
	if !allowed {
		visible := false
		if action != identitystore.OrgActionRead {
			visible, err = s.store.Identity().AuthorizeOrg(ctx, identitystore.AuthorizeOrgInput{
				Principal: principal,
				OrgID:     orgID,
				Action:    identitystore.OrgActionRead,
			})
			if err != nil {
				logent.AuthorizationCheckFailed(ctx, err)
				err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
				return identitystore.OrgRecord{}, &err
			}
		}
		if visible {
			logent.OrgAuthorization(ctx, authInput, logent.OrgAuthForbidden)
			err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
			return identitystore.OrgRecord{}, &err
		}
		logent.OrgAuthorization(ctx, authInput, logent.OrgAuthNotVisible)
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return identitystore.OrgRecord{}, &err
	}
	logent.OrgAuthorization(ctx, authInput, logent.OrgAuthAllowed)
	org, err := s.store.Identity().GetOrg(ctx, orgID)
	if err != nil {
		apiErr := apierror.OrgScoped(err)
		return identitystore.OrgRecord{}, &apiErr
	}
	logent.Org(ctx, org)
	return org, nil
}

func (s *Server) projectScope(
	ctx context.Context,
	orgIDRaw string,
	projectIDRaw string,
	action string,
) (identitystore.OrgRecord, identitystore.ProjectRecord, *apierror.ResponseError) {
	if s.store == nil {
		err := apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, &err
	}
	orgID, ok := parseOpenAPIPublicID(publicid.KindOrganization, orgIDRaw)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, &err
	}
	projectID, ok := parseOpenAPIPublicID(publicid.KindProject, projectIDRaw)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, &err
	}
	if scopeErr := s.authorizeProject(ctx, orgID, projectID, action); scopeErr != nil {
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, scopeErr
	}
	org, err := s.store.Identity().GetOrg(ctx, orgID)
	if err != nil {
		apiErr := apierror.OrgScoped(err)
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, &apiErr
	}
	project, err := s.store.Identity().GetProject(ctx, orgID, projectID)
	if err != nil {
		apiErr := apierror.ProjectScoped(err)
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, &apiErr
	}
	logent.Org(ctx, org)
	logent.Project(ctx, project)
	return org, project, nil
}

func (s *Server) authorizeProject(
	ctx context.Context,
	orgID storage.ID,
	projectID storage.ID,
	action string,
) *apierror.ResponseError {
	if s.store == nil {
		err := apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return &err
	}
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return &err
	}
	authInput := identitystore.AuthorizeProjectInput{
		Principal: principal,
		OrgID:     orgID,
		ProjectID: projectID,
		Action:    action,
	}
	allowed, err := s.store.Identity().AuthorizeProject(ctx, authInput)
	if err != nil {
		logent.AuthorizationCheckFailed(ctx, err)
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return &err
	}
	if !allowed {
		visible := false
		if action != identitystore.ProjectActionRead {
			visible, err = s.store.Identity().AuthorizeProject(ctx, identitystore.AuthorizeProjectInput{
				Principal: principal,
				OrgID:     orgID,
				ProjectID: projectID,
				Action:    identitystore.ProjectActionRead,
			})
			if err != nil {
				logent.AuthorizationCheckFailed(ctx, err)
				err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
				return &err
			}
		}
		if visible {
			logent.ProjectAuthorization(ctx, authInput, logent.ProjectAuthForbidden)
			err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
			return &err
		}
		logent.ProjectAuthorization(ctx, authInput, logent.ProjectAuthNotVisible)
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return &err
	}
	logent.ProjectAuthorization(ctx, authInput, logent.ProjectAuthAllowed)
	return nil
}

func (s *Server) orgManageAllowed(ctx context.Context, orgID storage.ID) bool {
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		return false
	}
	allowed, err := s.store.Identity().AuthorizeOrg(
		ctx,
		identitystore.AuthorizeOrgInput{Principal: principal, OrgID: orgID, Action: identitystore.OrgActionManage},
	)
	return err == nil && allowed
}

func (s *Server) machineScope(
	ctx context.Context,
	orgIDRaw string,
	machineIDRaw string,
	action string,
) (executionstore.MachineRecord, *apierror.ResponseError) {
	if s.store == nil {
		err := apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "store unavailable")
		return executionstore.MachineRecord{}, &err
	}
	orgID, ok := parseOpenAPIPublicID(publicid.KindOrganization, orgIDRaw)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return executionstore.MachineRecord{}, &err
	}
	machineID, ok := parseOpenAPIPublicID(publicid.KindMachine, machineIDRaw)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return executionstore.MachineRecord{}, &err
	}
	principal, ok := principalFromContext(ctx)
	if !ok || !identitystore.IsAccountPrincipal(principal) {
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return executionstore.MachineRecord{}, &err
	}
	authInput := executionstore.AuthorizeMachineInput{
		Principal: principal,
		OrgID:     orgID,
		MachineID: machineID,
		Action:    action,
	}
	allowed, err := s.store.Execution().AuthorizeMachine(ctx, authInput)
	if err != nil {
		logent.AuthorizationCheckFailed(ctx, err)
		err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return executionstore.MachineRecord{}, &err
	}
	if !allowed {
		orgAllowed, err := s.store.Identity().AuthorizeOrg(
			ctx,
			identitystore.AuthorizeOrgInput{Principal: principal, OrgID: orgID, Action: identitystore.OrgActionManage},
		)
		if err != nil {
			logent.AuthorizationCheckFailed(ctx, err)
			err := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
			return executionstore.MachineRecord{}, &err
		}
		if orgAllowed {
			if _, err := s.store.Execution().GetMachine(ctx, orgID, machineID); err != nil {
				apiErr := apierror.OrgScoped(err)
				return executionstore.MachineRecord{}, &apiErr
			}
		}
		logent.MachineAuthorization(ctx, authInput, logent.MachineAuthForbidden)
		apiErr := apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		return executionstore.MachineRecord{}, &apiErr
	}
	machine, err := s.store.Execution().GetMachine(ctx, orgID, machineID)
	if err != nil {
		apiErr := apierror.OrgScoped(err)
		return executionstore.MachineRecord{}, &apiErr
	}
	logent.MachineAuthorization(ctx, authInput, logent.MachineAuthAllowed)
	logent.Machine(ctx, machine)
	return machine, nil
}

func (s *Server) agentScope(
	ctx context.Context,
	orgIDRaw string,
	projectIDRaw string,
	agentIDRaw string,
	action string,
) (identitystore.OrgRecord, identitystore.ProjectRecord, executionstore.AgentRecord, *apierror.ResponseError) {
	org, project, scopeErr := s.projectScope(ctx, orgIDRaw, projectIDRaw, action)
	if scopeErr != nil {
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, executionstore.AgentRecord{}, scopeErr
	}
	agentID, ok := parseOpenAPIPublicID(publicid.KindAgent, agentIDRaw)
	if !ok {
		err := apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, executionstore.AgentRecord{}, &err
	}
	agent, err := s.store.Execution().GetAgentInProject(ctx, project.ID, agentID)
	if err != nil {
		apiErr := apierror.ProjectScoped(err)
		return identitystore.OrgRecord{}, identitystore.ProjectRecord{}, executionstore.AgentRecord{}, &apiErr
	}
	logent.Agent(ctx, agent)
	return org, project, agent, nil
}
