import { sdk } from '@omnara/sdk'
import * as schemas from '@omnara/sdk/zod'
import { op, type CommandGroup } from './factory.ts'
import { formatRecord, formatTable, formatVoid } from './format.ts'

export const commandGroups: CommandGroup[] = [
  {
    name: 'agents',
    aliases: ['agent'],
    summary: 'Launch and manage agents',
    operations: [
      op('list', 'List agents in a project', sdk.listAgents, {
        format: (response) =>
          formatTable(['id', 'name', 'state', 'model', 'created_at'])({
            data: response.data.map((agent) => ({ ...agent, model: agent.model?.name })),
            next_cursor: response.next_cursor,
          }),
        path: schemas.zListAgentsPath,
        query: schemas.zListAgentsQuery,
      }),
      op('get', 'Fetch an agent', sdk.getAgent, {
        format: formatRecord(), 
        path: schemas.zGetAgentPath
      }),
      op('launch', 'Launch a new agent', sdk.createAgent, {
        format: formatRecord(),
        path: schemas.zCreateAgentPath,
        body: schemas.zCreateAgentBody,
      }),
      op('cancel', 'Cancel a running agent', sdk.cancelAgent, {
        format: formatRecord(),
        path: schemas.zCancelAgentPath,
        body: schemas.zCancelAgentBody,
      }),
      op('archive', 'Archive an agent', sdk.archiveAgent, {
        format: formatRecord(),
        path: schemas.zArchiveAgentPath
      }),
    ],
  },
  {
    name: 'orgs',
    aliases: ['org'],
    summary: 'Manage organizations',
    operations: [
      op('create', 'Create an organization', sdk.createOrganization, {
        format: formatRecord(),
        body: schemas.zCreateOrganizationBody,
      }),
      op('overview', 'Show an organization overview', sdk.getOrgOverview, {
        format: formatRecord(),
        path: schemas.zGetOrgOverviewPath,
      }),
      op('delete', 'Delete an organization', sdk.deleteOrganization, {
        format: formatVoid('deleted'),
        path: schemas.zDeleteOrganizationPath,
        positional: ['orgID'],
      }),
    ],
  },
  {
    name: 'projects',
    aliases: ['project'],
    summary: 'Manage projects',
    operations: [
      op('list', 'List projects visible to you', sdk.listVisibleProjects, {
        format: formatTable(['id', 'name', 'created_at', 'updated_at']),
        path: schemas.zListVisibleProjectsPath,
        query: schemas.zListVisibleProjectsQuery,
      }),
      op('create', 'Create a project', sdk.createProject, {
        format: formatRecord(),
        path: schemas.zCreateProjectPath,
        body: schemas.zCreateProjectBody,
      }),
      op('delete', 'Delete a project', sdk.deleteProject, {
        format: formatVoid('deleted'),
        path: schemas.zDeleteProjectPath,
        positional: ['projectID'],
      }),
    ],
  },
  {
    name: 'members',
    aliases: ['member'],
    summary: 'Manage organization members and invitations',
    operations: [
      op('list', 'List organization members', sdk.listOrgMembers, {
        format: formatTable(['user_id', 'email', 'display_name', 'role', 'created_at']),
        path: schemas.zListOrgMembersPath,
        query: schemas.zListOrgMembersQuery,
      }),
      op("update", "Update a member's role", sdk.updateOrgMember, {
        format: formatRecord(),
        path: schemas.zUpdateOrgMemberPath,
        body: schemas.zUpdateOrgMemberBody,
      }),
      op('remove', 'Remove a member from the organization', sdk.removeOrgMember, {
        format: formatVoid('removed'),
        path: schemas.zRemoveOrgMemberPath,
      }),
    ],
    groups: [
      {
        name: 'invites',
        aliases: ['invite'],
        summary: 'Manage organization invitations',
        operations: [
          op('list', "List an organization's invitations", sdk.listOrgInvitations, {
            format: formatTable(['id', 'email', 'org_role', 'created_at']),
            path: schemas.zListOrgInvitationsPath,
            query: schemas.zListOrgInvitationsQuery,
          }),
          op('create', 'Invite someone to the organization', sdk.createOrgInvitation, {
            format: formatRecord(),
            path: schemas.zCreateOrgInvitationPath,
            body: schemas.zCreateOrgInvitationBody,
          }),
          op('delete', 'Withdraw an invitation', sdk.deleteOrgInvitation, {
            format: formatVoid('deleted'),
            path: schemas.zDeleteOrgInvitationPath,
          }),
          op('pending', 'List invitations waiting on you', sdk.listPendingInvitations, {
            format: formatTable(['id', 'org_name', 'email', 'org_role', 'created_at']),
            query: schemas.zListPendingInvitationsQuery,
          }),
          op('accept', 'Accept an invitation', sdk.acceptInvitation, {
            format: formatRecord(),
            path: schemas.zAcceptInvitationPath,
          }),
          op('decline', 'Decline an invitation', sdk.declineInvitation, {
            format: formatRecord(),
            path: schemas.zDeclineInvitationPath,
          }),
        ],
      },
    ],
  },
  {
    name: 'keys',
    aliases: ['key'],
    summary: 'Manage organization API keys and personal access tokens',
    groups: [
      {
        name: 'org',
        summary: 'Inspect organization API keys',
        operations: [
          op('list', 'List organization API keys', sdk.listOrgApiKeys, {
            format: formatTable(['id', 'name', 'org_role', 'created_at', 'last_used_at', 'revoked_at']),
            path: schemas.zListOrgApiKeysPath,
            query: schemas.zListOrgApiKeysQuery,
          }),
          op('get', 'Fetch an organization API key', sdk.getOrgApiKey, {
            format: formatRecord(), path: schemas.zGetOrgApiKeyPath
          }),
        ],
      },
      {
        name: 'personal',
        summary: 'Manage your personal access tokens',
        operations: [
          op('list', "List the authenticated user's personal access tokens", sdk.listPersonalAccessTokens, {
            query: schemas.zListPersonalAccessTokensQuery,
            format: formatTable(['id', 'name', 'created_at', 'last_used_at', 'revoked_at']),
          }),
          op('create', 'Create a personal access token', sdk.createPersonalAccessToken, {
            format: formatRecord(),
            body: schemas.zCreatePersonalAccessTokenBody,
          }),
          op('revoke', 'Revoke a personal access token', sdk.revokePersonalAccessToken, {
            format: formatRecord(),
            path: schemas.zRevokePersonalAccessTokenPath,
          }),
        ],
      },
    ],
  },
  {
    name: 'machines',
    aliases: ['machine'],
    summary: 'Manage machines',
    operations: [
      op('list', 'List machines visible to you', sdk.listVisibleMachines, {
        format: formatTable(['id', 'display_name', 'source_kind', 'provider', 'lifecycle_state', 'connection_state', 'created_at']),
        path: schemas.zListVisibleMachinesPath,
        query: schemas.zListVisibleMachinesQuery,
      }),
      op('get', 'Fetch a machine', sdk.getMachine, {
        format: formatRecord(), path: schemas.zGetMachinePath
      }),
      op('create', 'Create a machine', sdk.createMachine, {
        format: formatRecord(),
        path: schemas.zCreateMachinePath,
        body: schemas.zCreateMachineBody,
      }),
      op('update', 'Update a machine', sdk.updateMachine, {
        format: formatRecord(),
        path: schemas.zUpdateMachinePath,
        body: schemas.zUpdateMachineBody,
      }),
      op('delete', 'Delete a machine', sdk.deleteMachine, {
        format: formatVoid('deleted'), path: schemas.zDeleteMachinePath
      }),
    ],
  },
  {
    name: 'pools',
    aliases: ['pool'],
    summary: 'Manage machine pools',
    operations: [
      op('list', 'List machine pools', sdk.listMachinePools, {
        format: formatTable(['id', 'name', 'provider', 'max_total_machines', 'created_at']),
        path: schemas.zListMachinePoolsPath,
        query: schemas.zListMachinePoolsQuery,
      }),
      op('get', 'Fetch a machine pool', sdk.getMachinePool, {
        format: formatRecord(), path: schemas.zGetMachinePoolPath
      }),
      op('create', 'Create a machine pool', sdk.createMachinePool, {
        format: formatRecord(),
        path: schemas.zCreateMachinePoolPath,
        body: schemas.zCreateMachinePoolBody,
      }),
      op('update', 'Update a machine pool', sdk.updateMachinePool, {
        format: formatRecord(),
        path: schemas.zUpdateMachinePoolPath,
        body: schemas.zUpdateMachinePoolBody,
      }),
      op('delete', 'Delete a machine pool', sdk.deleteMachinePool, {
        format: formatVoid('deleted'), path: schemas.zDeleteMachinePoolPath
      }),
    ],
  },
  {
    name: 'model-providers',
    aliases: ['model-provider'],
    summary: 'Manage model provider configs',
    operations: [
      op('list', 'List model provider configs', sdk.listModelProviderConfigs, {
        format: formatTable(['id', 'name', 'api_format', 'base_url', 'created_at']),
        path: schemas.zListModelProviderConfigsPath,
        query: schemas.zListModelProviderConfigsQuery,
      }),
      op('get', 'Fetch a model provider config', sdk.getModelProviderConfig, {
        format: formatRecord(),
        path: schemas.zGetModelProviderConfigPath,
      }),
      op('create', 'Create a model provider config', sdk.createModelProviderConfig, {
        format: formatRecord(),
        path: schemas.zCreateModelProviderConfigPath,
        body: schemas.zCreateModelProviderConfigBody,
      }),
      op('update', 'Update a model provider config', sdk.updateModelProviderConfig, {
        format: formatRecord(),
        path: schemas.zUpdateModelProviderConfigPath,
        body: schemas.zUpdateModelProviderConfigBody,
      }),
      op('delete', 'Delete a model provider config', sdk.deleteModelProviderConfig, {
        format: formatVoid('deleted'),
        path: schemas.zDeleteModelProviderConfigPath,
      }),
      op('catalog', "Show a provider's model catalog", sdk.getModelCatalog, {
        format: formatRecord(),
        path: schemas.zGetModelCatalogPath,
      }),
    ],
  },
  {
    name: 'models',
    aliases: ['model'],
    summary: 'Manage configured models on a provider config',
    operations: [
      op('list', 'List configured models', sdk.listConfiguredModels, {
        format: formatTable(['id', 'name', 'provider_model_slug', 'context_window_tokens', 'created_at']),
        path: schemas.zListConfiguredModelsPath,
        query: schemas.zListConfiguredModelsQuery,
      }),
      op('create', 'Configure a model', sdk.createConfiguredModel, {
        format: formatRecord(),
        path: schemas.zCreateConfiguredModelPath,
        body: schemas.zCreateConfiguredModelBody,
      }),
      op('update', 'Update a configured model', sdk.updateConfiguredModel, {
        format: formatRecord(),
        path: schemas.zUpdateConfiguredModelPath,
        body: schemas.zUpdateConfiguredModelBody,
      }),
      op('delete', 'Remove a configured model', sdk.deleteConfiguredModel, {
        format: formatVoid('deleted'),
        path: schemas.zDeleteConfiguredModelPath,
      }),
    ],
  },
  {
    name: 'secrets',
    aliases: ['secret'],
    summary: 'Manage organization secrets',
    operations: [
      op('list', 'List secrets visible through ownership authority', sdk.listSecrets, {
        format: formatTable(['id', 'name', 'kind', 'current_version_number', 'created_at', 'updated_at']),
        path: schemas.zListSecretsPath,
        query: schemas.zListSecretsQuery,
      }),
      op('get', 'Fetch a secret', sdk.getSecret, {
        format: formatRecord(), path: schemas.zGetSecretPath
      }),
      op('create', 'Create a secret', sdk.createSecret, {
        format: formatRecord(),
        path: schemas.zCreateSecretPath,
        body: schemas.zCreateSecretBody,
      }),
      op('update', "Update a secret's name or metadata", sdk.updateSecret, {
        format: formatRecord(),
        path: schemas.zUpdateSecretPath,
        body: schemas.zUpdateSecretBody,
      }),
      op('delete', 'Delete a secret', sdk.deleteSecret, {
        format: formatVoid('deleted'), path: schemas.zDeleteSecretPath
      }),
    ],
  },
  {
    name: 'skills',
    aliases: ['skill'],
    summary: 'Manage skills',
    operations: [
      op('list', 'List skills', sdk.listSkills, {
        format: formatTable(['id', 'name', 'description', 'created_at', 'updated_at']),
        path: schemas.zListSkillsPath,
        query: schemas.zListSkillsQuery,
      }),
      op('get', 'Fetch a skill', sdk.getSkill, {
        format: formatRecord(), path: schemas.zGetSkillPath
      }),
      op('create', 'Create a skill', sdk.createSkill, {
        format: formatRecord(),
        path: schemas.zCreateSkillPath,
        body: schemas.zCreateSkillBody,
      }),
      op('delete', 'Delete a skill', sdk.deleteSkill, {
        format: formatVoid('deleted'), path: schemas.zDeleteSkillPath
      }),
    ],
  },
  {
    name: 'profiles',
    aliases: ['profile'],
    summary: 'Manage agent launch profiles',
    operations: [
      op('list', 'List agent profiles in a project', sdk.listAgentProfiles, {
        format: formatTable(['id', 'name', 'current_generation', 'created_at', 'updated_at']),
        path: schemas.zListAgentProfilesPath,
        query: schemas.zListAgentProfilesQuery,
      }),
      op('get', 'Fetch an agent profile', sdk.getAgentProfile, {
        format: formatRecord(), path: schemas.zGetAgentProfilePath
      }),
      op('create', 'Create an agent profile', sdk.createAgentProfile, {
        format: formatRecord(),
        path: schemas.zCreateAgentProfilePath,
        body: schemas.zCreateAgentProfileBody,
      }),
      op('rename', 'Rename an agent profile', sdk.renameAgentProfile, {
        format: formatRecord(),
        path: schemas.zRenameAgentProfilePath,
        body: schemas.zRenameAgentProfileBody,
      }),
      op('delete', 'Delete an agent profile', sdk.deleteAgentProfile, {
        format: formatVoid('deleted'),
        path: schemas.zDeleteAgentProfilePath,
      }),
    ],
  },
]
