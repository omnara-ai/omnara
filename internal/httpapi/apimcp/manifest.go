package apimcp

type Tool struct {
	Name        string
	Title       string
	OperationID string
	Description string
	Destructive bool
}

var Tools = []Tool{
	{Name: "whoami", Title: "Who am I", OperationID: "getCurrentUser"},

	{Name: "orgs_list", Title: "List organizations", OperationID: "listOrganizations"},
	{Name: "orgs_create", Title: "Create organization", OperationID: "createOrganization"},
	{Name: "orgs_overview", Title: "Get organization overview", OperationID: "getOrgOverview"},

	{Name: "projects_list", Title: "List projects", OperationID: "listVisibleProjects"},
	{Name: "projects_create", Title: "Create project", OperationID: "createProject"},

	{Name: "agents_list", Title: "List agents", OperationID: "listAgents"},
	{Name: "agents_get", Title: "Get agent", OperationID: "getAgent"},
	{Name: "agents_launch", Title: "Launch agent", OperationID: "createAgent"},
	{Name: "agents_update", Title: "Update agent config", OperationID: "updateAgentConfig"},

	{Name: "configs_get", Title: "Get agent config", OperationID: "getAgentConfig"},
	{Name: "configs_create", Title: "Create agent config", OperationID: "createAgentConfig"},
	{Name: "agents_input", Title: "Send agent input", OperationID: "createAgentInput"},
	{Name: "agents_cancel", Title: "Cancel agent", OperationID: "cancelAgent", Destructive: true},
	{Name: "agents_archive", Title: "Archive agent", OperationID: "archiveAgent", Destructive: true},
	{Name: "agents_events_list", Title: "List agent events", OperationID: "listEvents"},
	{Name: "agents_interactions_list", Title: "List agent interactions", OperationID: "listAgentInteractions"},
	{Name: "agents_interactions_resolve", Title: "Resolve agent interaction", OperationID: "resolveAgentInteraction"},

	{Name: "machines_list", Title: "List machines", OperationID: "listVisibleMachines"},
	{Name: "machines_get", Title: "Get machine", OperationID: "getMachine"},
	{Name: "machines_create", Title: "Connect BYO machine", OperationID: "connectBYOMachine"},
	{Name: "machines_update", Title: "Update machine", OperationID: "updateMachine"},
	{Name: "machines_delete", Title: "Delete machine", OperationID: "deleteMachine"},

	{Name: "pools_list", Title: "List machine pools", OperationID: "listMachinePools"},
	{Name: "pools_get", Title: "Get machine pool", OperationID: "getMachinePool"},
	{
		Name:        "pools_create",
		Title:       "Create machine pool",
		OperationID: "createMachinePool",
		Description: "Creates a machine pool in the organization. The body carries the pool name, the provider " +
			"(unikraft, modal, daytona, or blaxel), the provider auth secret, default machine settings, and " +
			"the per-machine and total CPU, memory, and machine-count limits the provider supports; the " +
			"required fields depend on the provider. See the createMachinePool operation in the Omnara API " +
			"reference at https://docs.omnara.com/api-reference for the full request schema.",
	},
	{Name: "pools_update", Title: "Update machine pool", OperationID: "updateMachinePool"},
	{Name: "pools_delete", Title: "Delete machine pool", OperationID: "deleteMachinePool"},

	{Name: "model_providers_list", Title: "List model provider configs", OperationID: "listModelProviderConfigs"},
	{Name: "model_providers_get", Title: "Get model provider config", OperationID: "getModelProviderConfig"},
	{Name: "model_providers_create", Title: "Create model provider config", OperationID: "createModelProviderConfig"},
	{Name: "model_providers_update", Title: "Update model provider config", OperationID: "updateModelProviderConfig"},
	{Name: "model_providers_delete", Title: "Delete model provider config", OperationID: "deleteModelProviderConfig"},
	{Name: "model_providers_catalog", Title: "Get model provider catalog", OperationID: "getModelCatalog"},

	{Name: "models_list", Title: "List configured models", OperationID: "listConfiguredModels"},
	{Name: "models_create", Title: "Create configured model", OperationID: "createConfiguredModel"},
	{Name: "models_update", Title: "Update configured model", OperationID: "updateConfiguredModel"},
	{Name: "models_delete", Title: "Delete configured model", OperationID: "deleteConfiguredModel"},

	{Name: "secrets_list", Title: "List secrets", OperationID: "listSecrets"},
	{Name: "secrets_get", Title: "Get secret", OperationID: "getSecret"},
	{Name: "secrets_create", Title: "Create secret", OperationID: "createSecret"},
	{Name: "secrets_update", Title: "Update secret", OperationID: "updateSecret"},
	{Name: "secrets_delete", Title: "Delete secret", OperationID: "deleteSecret"},

	{Name: "skills_list", Title: "List skills", OperationID: "listSkills"},
	{Name: "skills_get", Title: "Get skill", OperationID: "getSkill"},
	{Name: "skills_delete", Title: "Delete skill", OperationID: "deleteSkill"},

	{Name: "profiles_list", Title: "List agent profiles", OperationID: "listAgentProfiles"},
	{Name: "profiles_get", Title: "Get agent profile", OperationID: "getAgentProfile"},
	{Name: "profiles_create", Title: "Create agent profile", OperationID: "createAgentProfile"},
	{Name: "profiles_update", Title: "Update agent profile", OperationID: "updateAgentProfile"},
	{Name: "profiles_rename", Title: "Rename agent profile", OperationID: "renameAgentProfile"},
	{Name: "profiles_delete", Title: "Delete agent profile", OperationID: "deleteAgentProfile"},

	{Name: "crons_list", Title: "List cron triggers", OperationID: "listCronTriggers"},
	{Name: "crons_get", Title: "Get cron trigger", OperationID: "getCronTrigger"},
	{Name: "crons_create", Title: "Create cron trigger", OperationID: "createCronTrigger"},
	{Name: "crons_update", Title: "Update cron trigger", OperationID: "updateCronTrigger"},
	{Name: "crons_delete", Title: "Delete cron trigger", OperationID: "deleteCronTrigger"},

	{Name: "grant_skills_list", Title: "List skill grants", OperationID: "listSkillGrants"},
	{Name: "grant_skills_add", Title: "Grant skill to project", OperationID: "createSkillGrant"},
	{Name: "grant_skills_delete", Title: "Delete skill grant", OperationID: "deleteSkillGrant"},
	{Name: "grant_secrets_list", Title: "List secret grants", OperationID: "listSecretGrants"},
	{Name: "grant_secrets_add", Title: "Grant secret to project", OperationID: "createSecretGrant"},
	{Name: "grant_secrets_delete", Title: "Delete secret grant", OperationID: "deleteSecretGrant"},
	{Name: "grant_machines_list", Title: "List project machine grants", OperationID: "listProjectMachineGrants"},
	{Name: "grant_machines_add", Title: "Grant machine to project", OperationID: "createProjectMachineGrant"},
	{Name: "grant_machines_delete", Title: "Delete project machine grant", OperationID: "deleteProjectMachineGrant"},
	{Name: "grant_pools_list", Title: "List project machine pool grants", OperationID: "listProjectMachinePoolGrants"},
	{Name: "grant_pools_get", Title: "Get project machine pool grant", OperationID: "getProjectMachinePoolGrant"},
	{Name: "grant_pools_add", Title: "Grant machine pool to project", OperationID: "createProjectMachinePoolGrant"},
	{Name: "grant_pools_edit", Title: "Update project machine pool grant", OperationID: "updateProjectMachinePoolGrant"},
	{Name: "grant_pools_delete", Title: "Delete project machine pool grant", OperationID: "deleteProjectMachinePoolGrant"},
	{Name: "grant_models_list", Title: "List project model grants", OperationID: "listProjectModelGrants"},
	{Name: "grant_models_add", Title: "Grant model to project", OperationID: "createProjectModelGrant"},
	{Name: "grant_models_edit", Title: "Update project model grant", OperationID: "updateProjectModelGrant"},
	{Name: "grant_models_delete", Title: "Delete project model grant", OperationID: "deleteProjectModelGrant"},
}
