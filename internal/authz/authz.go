package authz

const (
	PrincipalUser              = "user"
	PrincipalOrgAPIKey         = "org_api_key"
	PrincipalAgent             = "agent"
	PrincipalMachineDaemon     = "machine_daemon"
	PrincipalMachinePoolDaemon = "machine_pool_daemon"
)

const (
	OrgRoleOwner  = "owner"
	OrgRoleAdmin  = "admin"
	OrgRoleMember = "member"
)

const (
	ProjectRoleAdmin     = "admin"
	ProjectRoleDeveloper = "developer"
	ProjectRoleOperator  = "operator"
	ProjectRoleViewer    = "viewer"
)

const (
	OrgRead   = "org.read"
	OrgManage = "org.manage"
	OrgOwn    = "org.own"

	OrgSecretsList   = "org.secrets.list"
	OrgSecretsManage = "org.secrets.manage"

	ProjectRead          = "project.read"
	ProjectManage        = "project.manage"
	ProjectAccessManage  = "project.access_manage"
	ProjectSecretsList   = "project.secrets.list"
	ProjectSecretsManage = "project.secrets.manage"

	AgentRead    = "agent.read"
	AgentOperate = "agent.operate"

	MachineRead   = "machine.read"
	MachineManage = "machine.manage"
)

var actionImplications = map[string][]string{
	OrgOwn:               {OrgManage},
	OrgManage:            {OrgRead, OrgSecretsManage},
	OrgSecretsManage:     {OrgSecretsList},
	ProjectManage:        {ProjectRead, ProjectSecretsManage},
	ProjectSecretsManage: {ProjectSecretsList},
	AgentOperate:         {AgentRead},
	MachineManage:        {MachineRead},
}

var orgRoleGrants = map[string][]string{
	OrgRoleOwner:  {OrgOwn},
	OrgRoleAdmin:  {OrgManage},
	OrgRoleMember: {OrgRead},
}

var projectRoleGrants = map[string][]string{
	ProjectRoleAdmin:     {ProjectAccessManage, ProjectManage, AgentOperate},
	ProjectRoleDeveloper: {ProjectManage, AgentOperate},
	ProjectRoleOperator:  {ProjectRead, AgentOperate},
	ProjectRoleViewer:    {ProjectRead, AgentRead},
}

func ActionImplies(granted, requested string) bool {
	return actionImplies(granted, requested, map[string]bool{})
}

func actionImplies(granted, requested string, seen map[string]bool) bool {
	if granted == requested {
		return true
	}
	if seen[granted] {
		return false
	}
	seen[granted] = true
	for _, implied := range actionImplications[granted] {
		if actionImplies(implied, requested, seen) {
			return true
		}
	}
	return false
}

func OrgRoleAllows(role, action string) bool {
	for _, granted := range orgRoleGrants[role] {
		if ActionImplies(granted, action) {
			return true
		}
	}
	return false
}

func ProjectRoleAllows(role, action string) bool {
	for _, granted := range projectRoleGrants[role] {
		if ActionImplies(granted, action) {
			return true
		}
	}
	return false
}

func MachineRoleAllows(action string) bool {
	return ActionImplies(MachineManage, action)
}
