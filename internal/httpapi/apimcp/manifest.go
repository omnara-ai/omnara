package apimcp

type Tool struct {
	Name        string
	OperationID string
	Description string
	Destructive bool
}

var Tools = []Tool{
	{Name: "whoami", OperationID: "getCurrentUser"},

	{Name: "orgs_list", OperationID: "listOrganizations"},
	{Name: "orgs_create", OperationID: "createOrganization"},
	{Name: "orgs_overview", OperationID: "getOrgOverview"},

	{Name: "projects_list", OperationID: "listVisibleProjects"},
	{Name: "projects_create", OperationID: "createProject"},

	{Name: "agents_list", OperationID: "listAgents"},
	{Name: "agents_get", OperationID: "getAgent"},
	{Name: "agents_launch", OperationID: "createAgent"},
	{Name: "agents_update", OperationID: "updateAgentConfig"},

	{Name: "configs_get", OperationID: "getAgentConfig"},
	{Name: "configs_create", OperationID: "createAgentConfig"},
	{Name: "agents_input", OperationID: "createAgentInput"},
	{Name: "agents_cancel", OperationID: "cancelAgent", Destructive: true},
	{Name: "agents_archive", OperationID: "archiveAgent", Destructive: true},
	{Name: "agents_events_list", OperationID: "listEvents"},
	{Name: "agents_interactions_list", OperationID: "listAgentInteractions"},
	{Name: "agents_interactions_resolve", OperationID: "resolveAgentInteraction"},

	{Name: "machines_list", OperationID: "listVisibleMachines"},
	{Name: "machines_get", OperationID: "getMachine"},
	{Name: "machines_create", OperationID: "connectBYOMachine"},
	{Name: "machines_update", OperationID: "updateMachine"},
	{Name: "machines_delete", OperationID: "deleteMachine"},

	{Name: "pools_list", OperationID: "listMachinePools"},
	{Name: "pools_get", OperationID: "getMachinePool"},
	{Name: "pools_create", OperationID: "createMachinePool"},
	{Name: "pools_update", OperationID: "updateMachinePool"},
	{Name: "pools_delete", OperationID: "deleteMachinePool"},

	{Name: "model_providers_list", OperationID: "listModelProviderConfigs"},
	{Name: "model_providers_get", OperationID: "getModelProviderConfig"},
	{Name: "model_providers_create", OperationID: "createModelProviderConfig"},
	{Name: "model_providers_update", OperationID: "updateModelProviderConfig"},
	{Name: "model_providers_delete", OperationID: "deleteModelProviderConfig"},
	{Name: "model_providers_catalog", OperationID: "getModelCatalog"},

	{Name: "models_list", OperationID: "listConfiguredModels"},
	{Name: "models_create", OperationID: "createConfiguredModel"},
	{Name: "models_update", OperationID: "updateConfiguredModel"},
	{Name: "models_delete", OperationID: "deleteConfiguredModel"},

	{Name: "secrets_list", OperationID: "listSecrets"},
	{Name: "secrets_get", OperationID: "getSecret"},
	{Name: "secrets_create", OperationID: "createSecret"},
	{Name: "secrets_update", OperationID: "updateSecret"},
	{Name: "secrets_delete", OperationID: "deleteSecret"},

	{Name: "skills_list", OperationID: "listSkills"},
	{Name: "skills_get", OperationID: "getSkill"},
	{Name: "skills_delete", OperationID: "deleteSkill"},

	{Name: "profiles_list", OperationID: "listAgentProfiles"},
	{Name: "profiles_get", OperationID: "getAgentProfile"},
	{Name: "profiles_create", OperationID: "createAgentProfile"},
	{Name: "profiles_update", OperationID: "updateAgentProfile"},
	{Name: "profiles_rename", OperationID: "renameAgentProfile"},
	{Name: "profiles_delete", OperationID: "deleteAgentProfile"},

	{Name: "crons_list", OperationID: "listCronTriggers"},
	{Name: "crons_get", OperationID: "getCronTrigger"},
	{Name: "crons_create", OperationID: "createCronTrigger"},
	{Name: "crons_update", OperationID: "updateCronTrigger"},
	{Name: "crons_delete", OperationID: "deleteCronTrigger"},

	{Name: "grant_skills_list", OperationID: "listSkillGrants"},
	{Name: "grant_skills_add", OperationID: "createSkillGrant"},
	{Name: "grant_skills_delete", OperationID: "deleteSkillGrant"},
	{Name: "grant_secrets_list", OperationID: "listSecretGrants"},
	{Name: "grant_secrets_add", OperationID: "createSecretGrant"},
	{Name: "grant_secrets_delete", OperationID: "deleteSecretGrant"},
	{Name: "grant_machines_list", OperationID: "listProjectMachineGrants"},
	{Name: "grant_machines_add", OperationID: "createProjectMachineGrant"},
	{Name: "grant_machines_delete", OperationID: "deleteProjectMachineGrant"},
	{Name: "grant_pools_list", OperationID: "listProjectMachinePoolGrants"},
	{Name: "grant_pools_get", OperationID: "getProjectMachinePoolGrant"},
	{Name: "grant_pools_add", OperationID: "createProjectMachinePoolGrant"},
	{Name: "grant_pools_edit", OperationID: "updateProjectMachinePoolGrant"},
	{Name: "grant_pools_delete", OperationID: "deleteProjectMachinePoolGrant"},
	{Name: "grant_models_list", OperationID: "listProjectModelGrants"},
	{Name: "grant_models_add", OperationID: "createProjectModelGrant"},
	{Name: "grant_models_edit", OperationID: "updateProjectModelGrant"},
	{Name: "grant_models_delete", OperationID: "deleteProjectModelGrant"},
}
