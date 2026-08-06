package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"reflect"
	"sort"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

type operationScopeKind int

const (
	scopeKindNone operationScopeKind = iota
	scopeKindOrg
	scopeKindProject
	scopeKindAgent
	scopeKindMachine
	scopeKindCustom
)

type operationPrincipalKind int

const (
	principalKindUser operationPrincipalKind = iota
	principalKindAccount
	principalKindBrowserSession
	principalKindMachineDaemon
	principalKindPublic
)

type operationScope struct {
	kind   operationScopeKind
	action string
	note   string
}

func noScope() operationScope { return operationScope{kind: scopeKindNone} }
func orgScope(action string) operationScope {
	return operationScope{kind: scopeKindOrg, action: action}
}
func projectScope(action string) operationScope {
	return operationScope{kind: scopeKindProject, action: action}
}
func agentScope(action string) operationScope {
	return operationScope{kind: scopeKindAgent, action: action}
}
func machineScope(action string) operationScope {
	return operationScope{kind: scopeKindMachine, action: action}
}
func customScope(note string) operationScope {
	return operationScope{kind: scopeKindCustom, note: note}
}

type operationID string

const (
	operationAcceptInvitation              operationID = "AcceptInvitation"
	operationBootstrapDaemon               operationID = "BootstrapDaemon"
	operationCancelAgent                   operationID = "CancelAgent"
	operationCancelQueuedBacklogInput      operationID = "CancelQueuedBacklogInput"
	operationCreateAgent                   operationID = "CreateAgent"
	operationCreateAgentConfig             operationID = "CreateAgentConfig"
	operationCreateAgentInput              operationID = "CreateAgentInput"
	operationCreateAgentProfile            operationID = "CreateAgentProfile"
	operationCreateBYOMachineDaemonToken   operationID = "CreateBYOMachineDaemonToken"
	operationCreateConfiguredModel         operationID = "CreateConfiguredModel"
	operationListToolCalls                 operationID = "ListToolCalls"
	operationSubmitToolCallResult          operationID = "SubmitToolCallResult"
	operationCreateIntegrationOAuthSetup   operationID = "CreateIntegrationOAuthSetup"
	operationCreateMachine                 operationID = "CreateMachine"
	operationCreateMachinePool             operationID = "CreateMachinePool"
	operationCreateModelProviderConfig     operationID = "CreateModelProviderConfig"
	operationCreateOrgAPIKey               operationID = "CreateOrgAPIKey"
	operationCreateOrgInvitation           operationID = "CreateOrgInvitation"
	operationCreateSecret                  operationID = "CreateSecret"
	operationCreateSecretGrant             operationID = "CreateSecretGrant"
	operationCreateSecretVersion           operationID = "CreateSecretVersion"
	operationDeleteSecret                  operationID = "DeleteSecret"
	operationDeleteSecretGrant             operationID = "DeleteSecretGrant"
	operationGetSecret                     operationID = "GetSecret"
	operationGetProjectAvailableSecret     operationID = "GetProjectAvailableSecret"
	operationListSecrets                   operationID = "ListSecrets"
	operationListSecretGrants              operationID = "ListSecretGrants"
	operationListProjectAvailableSecrets   operationID = "ListProjectAvailableSecrets"
	operationStartSecretMCPOAuth           operationID = "StartSecretMCPOAuth"
	operationUpdateSecret                  operationID = "UpdateSecret"
	operationCreateSkill                   operationID = "CreateSkill"
	operationCreateSkillGrant              operationID = "CreateSkillGrant"
	operationCreateOrganization            operationID = "CreateOrganization"
	operationCreatePersonalAccessToken     operationID = "CreatePersonalAccessToken"
	operationCreateProject                 operationID = "CreateProject"
	operationCreateProjectMachineGrant     operationID = "CreateProjectMachineGrant"
	operationCreateProjectMachinePoolGrant operationID = "CreateProjectMachinePoolGrant"
	operationCreateProjectModelGrant       operationID = "CreateProjectModelGrant"
	operationCreateSlackSetup              operationID = "CreateSlackSetup"
	operationDeclineInvitation             operationID = "DeclineInvitation"
	operationArchiveAgent                  operationID = "ArchiveAgent"
	operationDeleteAgentProfile            operationID = "DeleteAgentProfile"
	operationDeleteCurrentUser             operationID = "DeleteCurrentUser"
	operationDeleteIntegrationInstall      operationID = "DeleteIntegrationInstall"
	operationDeleteOrganization            operationID = "DeleteOrganization"
	operationDeleteProject                 operationID = "DeleteProject"
	operationDeleteConfiguredModel         operationID = "DeleteConfiguredModel"
	operationDeleteMachine                 operationID = "DeleteMachine"
	operationDeleteMachinePool             operationID = "DeleteMachinePool"
	operationDeleteModelProviderConfig     operationID = "DeleteModelProviderConfig"
	operationDeleteSkill                   operationID = "DeleteSkill"
	operationDeleteSkillGrant              operationID = "DeleteSkillGrant"
	operationDemoteSteeringInputToQueued   operationID = "DemoteSteeringInputToQueued"
	operationEndMachineDaemonRuntime       operationID = "EndMachineDaemonRuntime"
	operationGetAgent                      operationID = "GetAgent"
	operationGetAgentConfig                operationID = "GetAgentConfig"
	operationGetAgentProfile               operationID = "GetAgentProfile"
	operationGetArtifact                   operationID = "GetArtifact"
	operationGetArtifactContent            operationID = "GetArtifactContent"
	operationGetCurrentUser                operationID = "GetCurrentUser"
	operationGetDaemonSkillArchive         operationID = "GetDaemonSkillArchive"
	operationGetMachine                    operationID = "GetMachine"
	operationGetOrgAPIKey                  operationID = "GetOrgAPIKey"
	operationGetMachinePool                operationID = "GetMachinePool"
	operationGetModelProviderConfig        operationID = "GetModelProviderConfig"
	operationGetSkill                      operationID = "GetSkill"
	operationGetToolCatalog                operationID = "GetToolCatalog"
	operationGetProjectMachinePoolGrant    operationID = "GetProjectMachinePoolGrant"
	operationListActors                    operationID = "ListActors"
	operationGetActor                      operationID = "GetActor"
	operationPutActor                      operationID = "PutActor"
	operationListAgentInteractions         operationID = "ListAgentInteractions"
	operationListAgentProfiles             operationID = "ListAgentProfiles"
	operationListAgents                    operationID = "ListAgents"
	operationListBYOMachineDaemonTokens    operationID = "ListBYOMachineDaemonTokens"
	operationListConfiguredModels          operationID = "ListConfiguredModels"
	operationListEvents                    operationID = "ListEvents"
	operationListIntegrationInstalls       operationID = "ListIntegrationInstalls"
	operationListMachinePools              operationID = "ListMachinePools"
	operationListModelProviderConfigs      operationID = "ListModelProviderConfigs"
	operationListMemberProjectAccess       operationID = "ListMemberProjectAccess"
	operationListOrgAPIKeys                operationID = "ListOrgAPIKeys"
	operationListOrgInvitations            operationID = "ListOrgInvitations"
	operationListOrgMembers                operationID = "ListOrgMembers"
	operationListSkills                    operationID = "ListSkills"
	operationListSkillGrants               operationID = "ListSkillGrants"
	operationListProjectAvailableSkills    operationID = "ListProjectAvailableSkills"
	operationListPendingInvitations        operationID = "ListPendingInvitations"
	operationListPersonalAccessTokens      operationID = "ListPersonalAccessTokens"
	operationListProjectMachineGrants      operationID = "ListProjectMachineGrants"
	operationListProjectMachinePoolGrants  operationID = "ListProjectMachinePoolGrants"
	operationListProjectModelGrants        operationID = "ListProjectModelGrants"
	operationListQueuedBacklogInputs       operationID = "ListQueuedBacklogInputs"
	operationListTurnEvents                operationID = "ListTurnEvents"
	operationListTurns                     operationID = "ListTurns"
	operationListVisibleMachines           operationID = "ListVisibleMachines"
	operationListVisibleProjectMachines    operationID = "ListVisibleProjectMachines"
	operationListVisibleProjects           operationID = "ListVisibleProjects"
	operationMoveQueuedBacklogInput        operationID = "MoveQueuedBacklogInput"
	operationPromoteQueuedInputToSteering  operationID = "PromoteQueuedInputToSteering"
	operationRegisterMachineDaemonRuntime  operationID = "RegisterMachineDaemonRuntime"
	operationRecordDaemonBootstrapFailure  operationID = "RecordDaemonBootstrapFailure"
	operationRemoveMemberProjectAccess     operationID = "RemoveMemberProjectAccess"
	operationRemoveOrgMember               operationID = "RemoveOrgMember"
	operationResolveAgentInteraction       operationID = "ResolveAgentInteraction"
	operationRevokeMachineDaemonToken      operationID = "RevokeMachineDaemonToken"
	operationDeleteOrgInvitation           operationID = "DeleteOrgInvitation"
	operationRevokeOrgAPIKey               operationID = "RevokeOrgAPIKey"
	operationRevokePersonalAccessToken     operationID = "RevokePersonalAccessToken"
	operationDeleteProjectMachineGrant     operationID = "DeleteProjectMachineGrant"
	operationDeleteProjectMachinePoolGrant operationID = "DeleteProjectMachinePoolGrant"
	operationDeleteProjectModelGrant       operationID = "DeleteProjectModelGrant"
	operationSetMemberProjectAccess        operationID = "SetMemberProjectAccess"
	operationSleepMachineDaemonRuntime     operationID = "SleepMachineDaemonRuntime"
	operationSocketMachineDaemonRuntime    operationID = "SocketMachineDaemonRuntime"
	operationStreamEvents                  operationID = "StreamEvents"
	operationUpdateAgentConfig             operationID = "UpdateAgentConfig"
	operationUpdateAgentProfile            operationID = "UpdateAgentProfile"
	operationUpdateConfiguredModel         operationID = "UpdateConfiguredModel"
	operationUpdateMachinePool             operationID = "UpdateMachinePool"
	operationUpdateMachine                 operationID = "UpdateMachine"
	operationUpdateModelProviderConfig     operationID = "UpdateModelProviderConfig"
	operationUpdateOrgAPIKey               operationID = "UpdateOrgAPIKey"
	operationListOrgAPIKeyProjectAccess    operationID = "ListOrgAPIKeyProjectAccess"
	operationSetOrgAPIKeyProjectRole       operationID = "SetOrgAPIKeyProjectRole"
	operationRemoveOrgAPIKeyProjectRole    operationID = "RemoveOrgAPIKeyProjectRole"
	operationUpdateOrgMember               operationID = "UpdateOrgMember"
	operationUpdateProjectMachinePoolGrant operationID = "UpdateProjectMachinePoolGrant"
	operationUpdateProjectModelGrant       operationID = "UpdateProjectModelGrant"
)

type operationPolicy struct {
	principal operationPrincipalKind
	scope     operationScope
}

func userPolicy(scope operationScope) operationPolicy {
	return operationPolicy{principal: principalKindUser, scope: scope}
}

func accountPolicy(scope operationScope) operationPolicy {
	return operationPolicy{principal: principalKindAccount, scope: scope}
}
func browserSessionPolicy(scope operationScope) operationPolicy {
	return operationPolicy{principal: principalKindBrowserSession, scope: scope}
}
func machineDaemonPolicy(scope operationScope) operationPolicy {
	return operationPolicy{principal: principalKindMachineDaemon, scope: scope}
}

type operationAuthorizer map[operationID]operationPolicy

func (a operationAuthorizer) policy(operation operationID) (operationPolicy, bool) {
	policy, ok := a[operation]
	return policy, ok
}

var openAPIOperationPolicies = map[operationID]operationPolicy{
	operationGetCurrentUser:    userPolicy(noScope()),
	operationDeleteCurrentUser: userPolicy(noScope()),
	operationGetDaemonSkillArchive: machineDaemonPolicy(
		customScope("machine daemon token + machine-bound download capability"),
	),
	operationCreatePersonalAccessToken: browserSessionPolicy(noScope()),
	operationListPersonalAccessTokens:  userPolicy(noScope()),
	operationRevokePersonalAccessToken: userPolicy(noScope()),
	operationCreateOrgAPIKey:           browserSessionPolicy(orgScope(identitystore.OrgActionManage)),
	operationListOrgAPIKeys:            accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationGetOrgAPIKey:              accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationUpdateOrgAPIKey:           browserSessionPolicy(orgScope(identitystore.OrgActionManage)),
	operationRevokeOrgAPIKey:           browserSessionPolicy(orgScope(identitystore.OrgActionManage)),
	operationListOrgAPIKeyProjectAccess: accountPolicy(
		orgScope(identitystore.OrgActionManage),
	),
	operationSetOrgAPIKeyProjectRole: browserSessionPolicy(
		projectScope(identitystore.ProjectActionAccessManage),
	),
	operationRemoveOrgAPIKeyProjectRole: browserSessionPolicy(
		projectScope(identitystore.ProjectActionAccessManage),
	),
	operationListPendingInvitations: userPolicy(noScope()),
	operationAcceptInvitation:       userPolicy(noScope()),
	operationDeclineInvitation:      userPolicy(noScope()),
	operationCreateOrganization:     userPolicy(noScope()),

	operationCreateProject:              accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationDeleteProject:              accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationDeleteOrganization:         accountPolicy(orgScope(identitystore.OrgActionOwn)),
	operationListOrgInvitations:         accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationCreateOrgInvitation:        accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationDeleteOrgInvitation:        accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationUpdateOrgMember:            accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationRemoveOrgMember:            accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationListMemberProjectAccess:    accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationCreateMachine:              accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationListMachinePools:           accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationCreateMachinePool:          accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationGetMachinePool:             accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationUpdateMachinePool:          accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationDeleteMachinePool:          accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationCreateModelProviderConfig:  accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationListModelProviderConfigs:   accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationGetModelProviderConfig:     accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationUpdateModelProviderConfig:  accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationDeleteModelProviderConfig:  accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationCreateConfiguredModel:      accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationListConfiguredModels:       accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationUpdateConfiguredModel:      accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationDeleteConfiguredModel:      accountPolicy(orgScope(identitystore.OrgActionManage)),
	operationListOrgMembers:             accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListVisibleProjects:        accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListVisibleMachines:        accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationCreateSecret:               accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationCreateSecretGrant:          accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationCreateSecretVersion:        accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationDeleteSecret:               accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationDeleteSecretGrant:          accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationGetSecret:                  accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListSecrets:                accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListSecretGrants:           accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationStartSecretMCPOAuth:        userPolicy(orgScope(identitystore.OrgActionRead)),
	operationUpdateSecret:               accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationCreateSkill:                accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationCreateSkillGrant:           accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationDeleteSkill:                accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationDeleteSkillGrant:           accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationGetSkill:                   accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListSkills:                 accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListSkillGrants:            accountPolicy(orgScope(identitystore.OrgActionRead)),
	operationListProjectAvailableSkills: accountPolicy(projectScope(identitystore.ProjectActionRead)),

	operationCreateAgentConfig:             accountPolicy(projectScope(identitystore.ProjectActionManage)),
	operationDeleteIntegrationInstall:      accountPolicy(projectScope(identitystore.ProjectActionManage)),
	operationCreateAgentProfile:            accountPolicy(projectScope(identitystore.ProjectActionManage)),
	operationUpdateAgentProfile:            accountPolicy(projectScope(identitystore.ProjectActionManage)),
	operationDeleteAgentProfile:            accountPolicy(projectScope(identitystore.ProjectActionManage)),
	operationCreateIntegrationOAuthSetup:   userPolicy(projectScope(identitystore.ProjectActionManage)),
	operationCreateSlackSetup:              userPolicy(projectScope(identitystore.ProjectActionManage)),
	operationGetAgentConfig:                accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationGetToolCatalog:                accountPolicy(noScope()),
	operationGetAgentProfile:               accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationListAgentProfiles:             accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationListIntegrationInstalls:       accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationListVisibleProjectMachines:    accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationListAgents:                    accountPolicy(projectScope(identitystore.AgentActionRead)),
	operationListActors:                    accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationGetActor:                      accountPolicy(projectScope(identitystore.ProjectActionRead)),
	operationPutActor:                      accountPolicy(projectScope(identitystore.ProjectActionManage)),
	operationCreateAgent:                   accountPolicy(projectScope(identitystore.AgentActionOperate)),
	operationCreateProjectModelGrant:       accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationSetMemberProjectAccess:        accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationRemoveMemberProjectAccess:     accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationListProjectAvailableSecrets:   accountPolicy(projectScope(identitystore.ProjectActionSecretsList)),
	operationGetProjectAvailableSecret:     accountPolicy(projectScope(identitystore.ProjectActionSecretsList)),
	operationListProjectModelGrants:        accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationUpdateProjectModelGrant:       accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationDeleteProjectModelGrant:       accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationListProjectMachineGrants:      accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationListProjectMachinePoolGrants:  accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationGetProjectMachinePoolGrant:    accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationUpdateProjectMachinePoolGrant: accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationDeleteProjectMachinePoolGrant: accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),
	operationCreateProjectMachinePoolGrant: accountPolicy(projectScope(identitystore.ProjectActionAccessManage)),

	operationGetAgent:                     accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationListQueuedBacklogInputs:      accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationListEvents:                   accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationListTurns:                    accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationListTurnEvents:               accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationStreamEvents:                 accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationListAgentInteractions:        accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationGetArtifact:                  accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationGetArtifactContent:           accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationCancelAgent:                  accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationArchiveAgent:                 accountPolicy(agentScope(identitystore.ProjectActionManage)),
	operationCancelQueuedBacklogInput:     accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationMoveQueuedBacklogInput:       accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationPromoteQueuedInputToSteering: accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationDemoteSteeringInputToQueued:  accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationCreateAgentInput:             accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationListToolCalls:                accountPolicy(agentScope(identitystore.AgentActionRead)),
	operationSubmitToolCallResult:         accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationResolveAgentInteraction:      accountPolicy(agentScope(identitystore.AgentActionOperate)),
	operationUpdateAgentConfig:            accountPolicy(agentScope(identitystore.ProjectActionManage)),

	operationGetMachine:                  accountPolicy(machineScope(executionstore.MachineActionRead)),
	operationUpdateMachine:               accountPolicy(machineScope(executionstore.MachineActionManage)),
	operationDeleteMachine:               accountPolicy(machineScope(executionstore.MachineActionManage)),
	operationListBYOMachineDaemonTokens:  accountPolicy(machineScope(executionstore.MachineActionManage)),
	operationCreateBYOMachineDaemonToken: accountPolicy(machineScope(executionstore.MachineActionManage)),
	operationRevokeMachineDaemonToken:    accountPolicy(machineScope(executionstore.MachineActionManage)),

	operationCreateProjectMachineGrant: accountPolicy(customScope("project access-manage + BYO machine manage")),
	operationDeleteProjectMachineGrant: accountPolicy(
		customScope("project access-manage + machine manage on the grant's machine"),
	),
	operationBootstrapDaemon: machineDaemonPolicy(customScope("machine daemon token (bootstrap derives binding)")),
	operationRecordDaemonBootstrapFailure: machineDaemonPolicy(
		customScope("machine daemon token (token-derived machine)"),
	),
	operationRegisterMachineDaemonRuntime: machineDaemonPolicy(
		customScope("machine daemon token (token-derived machine)"),
	),
	operationEndMachineDaemonRuntime: machineDaemonPolicy(
		customScope("machine daemon token + token-scoped runtime ownership"),
	),
	operationSleepMachineDaemonRuntime: machineDaemonPolicy(
		customScope("machine daemon token + token-scoped runtime ownership"),
	),
	operationSocketMachineDaemonRuntime: machineDaemonPolicy(customScope("daemon runtime websocket upgrade")),
}

func newOpenAPIAuthorizer() (operationAuthorizer, error) {
	return buildOpenAPIAuthorizer(generatedStrictOpenAPIOperations(), openAPIOperationPolicies)
}

func buildOpenAPIAuthorizer(
	operations []operationID,
	policies map[operationID]operationPolicy,
) (operationAuthorizer, error) {
	operationSet := make(map[operationID]struct{}, len(operations))
	var duplicates []string
	for _, operation := range operations {
		if _, ok := operationSet[operation]; ok {
			duplicates = append(duplicates, string(operation))
			continue
		}
		operationSet[operation] = struct{}{}
	}
	var missing []string
	for operation := range operationSet {
		if _, ok := policies[operation]; !ok {
			missing = append(missing, string(operation))
		}
	}
	var unknown []string
	for operation := range policies {
		if _, ok := operationSet[operation]; !ok {
			unknown = append(unknown, string(operation))
		}
	}
	if len(duplicates) > 0 || len(missing) > 0 || len(unknown) > 0 {
		sort.Strings(duplicates)
		sort.Strings(missing)
		sort.Strings(unknown)
		return operationAuthorizer{}, fmt.Errorf(
			"openapi operation policy mismatch: duplicate=%v missing=%v unknown=%v",
			duplicates,
			missing,
			unknown,
		)
	}
	copied := make(operationAuthorizer, len(policies))
	for operation, policy := range policies {
		copied[operation] = policy
	}
	return copied, nil
}

func generatedStrictOpenAPIOperations() []operationID {
	ifaceType := reflect.TypeOf((*openapi.StrictServerInterface)(nil)).Elem()
	operations := make([]operationID, 0, ifaceType.NumMethod())
	for i := range ifaceType.NumMethod() {
		operations = append(operations, operationID(ifaceType.Method(i).Name))
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i] < operations[j]
	})
	return operations
}

func (s *Server) authorizeOperation(
	ctx context.Context,
	r *http.Request,
	policy operationPolicy,
) (context.Context, error) {
	if err := authorizeOperationPrincipal(ctx, policy.principal); err != nil {
		return nil, err
	}
	switch policy.scope.kind {
	case scopeKindNone, scopeKindCustom:
		return ctx, nil
	case scopeKindOrg:
		org, scopeErr := s.orgScope(ctx, r.PathValue("orgID"), policy.scope.action)
		if scopeErr != nil {
			return nil, *scopeErr
		}
		return withOrgScope(ctx, org), nil
	case scopeKindProject:
		org, project, scopeErr := s.projectScope(
			ctx,
			r.PathValue("orgID"),
			r.PathValue("projectID"),
			policy.scope.action,
		)
		if scopeErr != nil {
			return nil, *scopeErr
		}
		return withProjectScope(ctx, org, project), nil
	case scopeKindAgent:
		org, project, agent, scopeErr := s.agentScope(
			ctx,
			r.PathValue("orgID"),
			r.PathValue("projectID"),
			r.PathValue("agentID"),
			policy.scope.action,
		)
		if scopeErr != nil {
			return nil, *scopeErr
		}
		return withAgentScope(ctx, org, project, agent), nil
	case scopeKindMachine:
		machine, scopeErr := s.machineScope(ctx, r.PathValue("orgID"), r.PathValue("machineID"), policy.scope.action)
		if scopeErr != nil {
			return nil, *scopeErr
		}
		return withMachineScope(ctx, machine), nil
	default:
		return nil, apierror.FromCode(openapi.ErrorCodeInternalError, "unhandled operation scope")
	}
}

func authorizeOperationPrincipal(ctx context.Context, kind operationPrincipalKind) error {
	if kind == principalKindPublic {
		return nil
	}
	principal, ok := principalFromContext(ctx)
	if !ok {
		return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
	}
	switch kind {
	case principalKindUser:
		if principal.Type != identitystore.PrincipalTypeUser {
			return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		}
	case principalKindAccount:
		if !identitystore.IsAccountPrincipal(principal) {
			return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		}
	case principalKindBrowserSession:
		if principal.Type != identitystore.PrincipalTypeUser || principal.BrowserSessionID == storage.NilID {
			return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		}
	case principalKindMachineDaemon:
		if principal.Type != identitystore.PrincipalTypeMachineDaemon {
			return apierror.FromCode(openapi.ErrorCodeForbidden, "forbidden")
		}
	default:
		return apierror.FromCode(openapi.ErrorCodeInternalError, "unhandled operation principal")
	}
	return nil
}

type orgScopeContextKey struct{}
type projectScopeContextKey struct{}
type agentScopeContextKey struct{}
type machineScopeContextKey struct{}

type projectScopeRecord struct {
	org     identitystore.OrgRecord
	project identitystore.ProjectRecord
}

type agentScopeRecord struct {
	org     identitystore.OrgRecord
	project identitystore.ProjectRecord
	agent   executionstore.AgentRecord
}

func withOrgScope(ctx context.Context, org identitystore.OrgRecord) context.Context {
	return context.WithValue(ctx, orgScopeContextKey{}, org)
}

func missingOperationScopeError(scope string) error {
	return apierror.FromCode(openapi.ErrorCodeInternalError, "missing "+scope+" operation scope")
}

func orgScopeFromContext(ctx context.Context) (identitystore.OrgRecord, error) {
	org, ok := ctx.Value(orgScopeContextKey{}).(identitystore.OrgRecord)
	if !ok || org.ID == storage.NilID {
		return identitystore.OrgRecord{}, missingOperationScopeError("organization")
	}
	return org, nil
}

func withProjectScope(
	ctx context.Context,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
) context.Context {
	return context.WithValue(ctx, projectScopeContextKey{}, projectScopeRecord{org: org, project: project})
}

func projectScopeFromContext(ctx context.Context) (projectScopeRecord, error) {
	record, ok := ctx.Value(projectScopeContextKey{}).(projectScopeRecord)
	if !ok || record.org.ID == storage.NilID || record.project.ID == storage.NilID {
		return projectScopeRecord{}, missingOperationScopeError("project")
	}
	return record, nil
}

func withAgentScope(
	ctx context.Context,
	org identitystore.OrgRecord,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) context.Context {
	return context.WithValue(ctx, agentScopeContextKey{}, agentScopeRecord{org: org, project: project, agent: agent})
}

func agentScopeFromContext(ctx context.Context) (agentScopeRecord, error) {
	record, ok := ctx.Value(agentScopeContextKey{}).(agentScopeRecord)
	if !ok || record.org.ID == storage.NilID || record.project.ID == storage.NilID || record.agent.ID == storage.NilID {
		return agentScopeRecord{}, missingOperationScopeError("agent")
	}
	return record, nil
}

func withMachineScope(ctx context.Context, machine executionstore.MachineRecord) context.Context {
	return context.WithValue(ctx, machineScopeContextKey{}, machine)
}

func machineScopeFromContext(ctx context.Context) (executionstore.MachineRecord, error) {
	machine, ok := ctx.Value(machineScopeContextKey{}).(executionstore.MachineRecord)
	if !ok || machine.ID == storage.NilID {
		return executionstore.MachineRecord{}, missingOperationScopeError("machine")
	}
	return machine, nil
}
