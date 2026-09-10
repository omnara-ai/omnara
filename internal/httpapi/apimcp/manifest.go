package apimcp

type Tool struct {
	Name        string
	OperationID string
	Description string
	ReadOnly    bool
	Destructive bool
}

var Tools = []Tool{
	{Name: "whoami", OperationID: "getCurrentUser", ReadOnly: true},

	{Name: "orgs_list", OperationID: "listOrganizations", ReadOnly: true},
	{Name: "orgs_create", OperationID: "createOrganization"},
	{Name: "orgs_overview", OperationID: "getOrgOverview", ReadOnly: true},

	{Name: "projects_list", OperationID: "listVisibleProjects", ReadOnly: true},
	{Name: "projects_create", OperationID: "createProject"},

	{Name: "agents_list", OperationID: "listAgents", ReadOnly: true},
	{Name: "agents_get", OperationID: "getAgent", ReadOnly: true},
	{Name: "agents_launch", OperationID: "createAgent"},
	{Name: "agents_update", OperationID: "updateAgentConfig"},
	{Name: "agents_input", OperationID: "createAgentInput"},
	{Name: "agents_cancel", OperationID: "cancelAgent", Destructive: true},
	{Name: "agents_archive", OperationID: "archiveAgent", Destructive: true},
	{Name: "agents_events_list", OperationID: "listEvents", ReadOnly: true},
	{Name: "agents_interactions_list", OperationID: "listAgentInteractions", ReadOnly: true},
	{Name: "agents_interactions_resolve", OperationID: "resolveAgentInteraction"},

	{Name: "machines_list", OperationID: "listVisibleMachines", ReadOnly: true},
	{Name: "machines_get", OperationID: "getMachine", ReadOnly: true},
	{Name: "machines_create", OperationID: "connectBYOMachine"},
	{Name: "machines_update", OperationID: "updateMachine"},
	{Name: "machines_delete", OperationID: "deleteMachine", Destructive: true},

	{Name: "pools_list", OperationID: "listMachinePools", ReadOnly: true},
	{Name: "pools_get", OperationID: "getMachinePool", ReadOnly: true},
	{Name: "pools_create", OperationID: "createMachinePool"},
	{Name: "pools_update", OperationID: "updateMachinePool"},
	{Name: "pools_delete", OperationID: "deleteMachinePool", Destructive: true},

	{Name: "model_providers_list", OperationID: "listModelProviderConfigs", ReadOnly: true},
	{Name: "model_providers_get", OperationID: "getModelProviderConfig", ReadOnly: true},
	{Name: "model_providers_create", OperationID: "createModelProviderConfig"},
	{Name: "model_providers_update", OperationID: "updateModelProviderConfig"},
	{Name: "model_providers_delete", OperationID: "deleteModelProviderConfig", Destructive: true},
	{Name: "model_providers_catalog", OperationID: "getModelCatalog", ReadOnly: true},

	{Name: "models_list", OperationID: "listConfiguredModels", ReadOnly: true},
	{Name: "models_create", OperationID: "createConfiguredModel"},
	{Name: "models_update", OperationID: "updateConfiguredModel"},
	{Name: "models_delete", OperationID: "deleteConfiguredModel", Destructive: true},

	{Name: "secrets_list", OperationID: "listSecrets", ReadOnly: true},
	{Name: "secrets_get", OperationID: "getSecret", ReadOnly: true},
	{Name: "secrets_create", OperationID: "createSecret"},
	{Name: "secrets_update", OperationID: "updateSecret"},
	{Name: "secrets_delete", OperationID: "deleteSecret", Destructive: true},

	{Name: "skills_list", OperationID: "listSkills", ReadOnly: true},
	{Name: "skills_get", OperationID: "getSkill", ReadOnly: true},
	{Name: "skills_delete", OperationID: "deleteSkill", Destructive: true},

	{Name: "profiles_list", OperationID: "listAgentProfiles", ReadOnly: true},
	{Name: "profiles_get", OperationID: "getAgentProfile", ReadOnly: true},
	{Name: "profiles_create", OperationID: "createAgentProfile"},
	{Name: "profiles_update", OperationID: "updateAgentProfile"},
	{Name: "profiles_rename", OperationID: "renameAgentProfile"},
	{Name: "profiles_delete", OperationID: "deleteAgentProfile", Destructive: true},

	{Name: "crons_list", OperationID: "listCronTriggers", ReadOnly: true},
	{Name: "crons_get", OperationID: "getCronTrigger", ReadOnly: true},
	{Name: "crons_create", OperationID: "createCronTrigger"},
	{Name: "crons_update", OperationID: "updateCronTrigger"},
	{Name: "crons_delete", OperationID: "deleteCronTrigger", Destructive: true},

	{Name: "grant_skills_list", OperationID: "listSkillGrants", ReadOnly: true},
	{Name: "grant_skills_add", OperationID: "createSkillGrant"},
	{Name: "grant_skills_delete", OperationID: "deleteSkillGrant", Destructive: true},
	{Name: "grant_secrets_list", OperationID: "listSecretGrants", ReadOnly: true},
	{Name: "grant_secrets_add", OperationID: "createSecretGrant"},
	{Name: "grant_secrets_delete", OperationID: "deleteSecretGrant", Destructive: true},
	{Name: "grant_machines_list", OperationID: "listProjectMachineGrants", ReadOnly: true},
	{Name: "grant_machines_add", OperationID: "createProjectMachineGrant"},
	{Name: "grant_machines_delete", OperationID: "deleteProjectMachineGrant", Destructive: true},
	{Name: "grant_pools_list", OperationID: "listProjectMachinePoolGrants", ReadOnly: true},
	{Name: "grant_pools_get", OperationID: "getProjectMachinePoolGrant", ReadOnly: true},
	{Name: "grant_pools_add", OperationID: "createProjectMachinePoolGrant"},
	{Name: "grant_pools_edit", OperationID: "updateProjectMachinePoolGrant"},
	{Name: "grant_pools_delete", OperationID: "deleteProjectMachinePoolGrant", Destructive: true},
	{Name: "grant_models_list", OperationID: "listProjectModelGrants", ReadOnly: true},
	{Name: "grant_models_add", OperationID: "createProjectModelGrant"},
	{Name: "grant_models_edit", OperationID: "updateProjectModelGrant"},
	{Name: "grant_models_delete", OperationID: "deleteProjectModelGrant", Destructive: true},
}
