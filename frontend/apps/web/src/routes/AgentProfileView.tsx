import {
  useAgentProfile,
  useCreateAgent,
  useCreateAgentConfig,
  useDeleteAgentProfile,
  useUpdateAgentProfile,
} from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { AgentConfigYamlField } from '@/components/agents/AgentConfigYamlField'
import { AgentProfileIntegrations } from '@/components/agents/AgentProfileIntegrations'
import { AgentsTable } from '@/components/agents/AgentsSection'
import { DeployAgentProfileDialog } from '@/components/agents/DeployAgentProfileDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { SlackOAuthOutcomeDialog } from '@/components/agents/SlackOAuthOutcomeDialog'
import { DetailList } from '@/components/data-table/DetailList'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Button } from '@/components/ui/button'
import { FieldGroup } from '@/components/ui/field'
import { formatDateTime } from '@/lib/format'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

type ProfileTab = 'configuration' | 'agents'

interface ConfigDraft {
  configId: string
  yaml: string
}

export function AgentProfileView() {
  const { activeOrg } = useActiveOrg()
  const params = useParams({ strict: false })
  const projectId = params.projectId ?? ''
  const profileId = params.profileId ?? ''
  const { data: profile } = useAgentProfile(activeOrg.id, projectId, profileId)

  return <ProfileView key={profile.id} profile={profile} projectId={projectId} />
}

function ProfileView({ profile, projectId }: { profile: AgentProfile; projectId: string }) {
  const { activeOrg } = useActiveOrg()
  const { project } = useProjectPage()
  const canOperate = project?.access.can_operate ?? false
  const canManage = project?.access.can_manage ?? false

  const [tab, setTab] = useState<ProfileTab>('configuration')
  const [deployOpen, setDeployOpen] = useState(false)
  const [draft, setDraft] = useState<ConfigDraft | null>(null)

  const savedYaml = profile.current_config.source ?? ''
  const activeDraft = draft?.configId === profile.current_config_id ? draft : null
  const yaml = activeDraft?.yaml ?? savedYaml
  const configDirty = yaml !== savedYaml

  const createAgent = useCreateAgent(activeOrg.id, projectId)
  const createConfig = useCreateAgentConfig(activeOrg.id, projectId)
  const updateProfile = useUpdateAgentProfile(activeOrg.id, projectId)
  const deleteProfile = useDeleteAgentProfile(activeOrg.id, projectId)
  const navigate = useNavigate()

  const [saveError, setSaveError] = useState('')
  const [saveErrorConfigId, setSaveErrorConfigId] = useState(profile.current_config_id)
  const savePending = createConfig.isPending || updateProfile.isPending

  if (saveErrorConfigId !== profile.current_config_id) {
    setSaveErrorConfigId(profile.current_config_id)
    setSaveError('')
  }

  async function saveRevision() {
    setSaveError('')
    try {
      const config = await createConfig.mutateAsync({ source: yaml, source_format: 'yaml' })
      await updateProfile.mutateAsync({
        agentProfileID: profile.id,
        config: config.id,
        expected_current_config_id: profile.current_config_id,
      })
      setDraft(null)
    } catch (err) {
      setSaveError(err instanceof ApiError ? err.message : 'Could not update agent profile')
    }
  }

  async function launch() {
    if (
      configDirty &&
      !window.confirm(
        'You have unsaved configuration changes. Launch uses the last saved revision. Continue?',
      )
    ) {
      return
    }
    try {
      const launched = await createAgent.mutateAsync({
        profile: profile.id,
        config: profile.current_config_id,
      })
      await navigate({
        to: '/projects/$projectId/agents/$agentId',
        params: { projectId, agentId: launched.agent.id },
      })
    } catch (error) {
      window.alert(error instanceof ApiError ? error.message : 'Could not launch agent')
    }
  }

  function remove() {
    if (!window.confirm(`Delete agent profile ${profile.name}?`)) return
    deleteProfile.mutate(profile.id, {
      onSuccess: () => {
        void navigate({ to: '/projects/$projectId/agents', params: { projectId } })
      },
      onError: (error) => {
        window.alert(error instanceof ApiError ? error.message : 'Could not delete agent profile')
      },
    })
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-6">
      <header className="flex flex-col gap-4">
        <PageBreadcrumb
          items={[
            { id: 'organization', label: activeOrg.name, to: '/' },
            ...(project ? [{ id: 'project', label: project.name }] : []),
            {
              id: 'agents',
              label: 'Agents',
              to: '/projects/$projectId/agents' as const,
              params: { projectId },
            },
            { id: 'profile', label: profile.name },
          ]}
        />
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="flex min-w-0 flex-col gap-1">
            <h1 className="text-2xl font-bold tracking-tight">{profile.name}</h1>
            <p className="text-muted-foreground text-sm">
              <span className="font-mono text-xs">{profile.id}</span>
              {' · '}Updated {formatDateTime(profile.updated_at)}
            </p>
          </div>
          <div className="flex items-center gap-2">
            {canOperate && (
              <Button
                size="sm"
                disabled={createAgent.isPending}
                loading={createAgent.isPending}
                onClick={() => void launch()}
              >
                Launch
              </Button>
            )}
            {canManage && (
              <ResourceRowActions
                onGrant={() => {
                  setDeployOpen(true)
                }}
                grantLabel="Deploy to app"
                onDelete={remove}
              />
            )}
          </div>
        </div>
        <PillTabs
          value={tab}
          onValueChange={setTab}
          tabs={[
            { value: 'configuration', label: 'Configuration' },
            { value: 'agents', label: 'Agents' },
          ]}
        />
      </header>

      {tab === 'configuration' ? (
        <ConfigurationTab
          orgId={activeOrg.id}
          projectId={projectId}
          profile={profile}
          canManage={canManage}
          yaml={yaml}
          dirty={configDirty}
          error={saveError}
          pending={savePending}
          onYamlChange={(value) => {
            setDraft({ configId: profile.current_config_id, yaml: value })
          }}
          onDiscard={() => {
            setDraft(null)
            setSaveError('')
          }}
          onSave={() => {
            void saveRevision()
          }}
        />
      ) : (
        <AgentsTable
          orgId={activeOrg.id}
          projectId={projectId}
          canManage={canManage}
          profileId={profile.id}
          emptyMessage="No agents from this profile yet. Launch one to get started."
        />
      )}

      {canManage && deployOpen && (
        <DeployAgentProfileDialog
          open
          onOpenChange={setDeployOpen}
          orgId={activeOrg.id}
          projectId={projectId}
          profile={profile}
        />
      )}
      <SlackOAuthOutcomeDialog />
    </div>
  )
}

function ConfigurationTab({
  orgId,
  projectId,
  profile,
  canManage,
  yaml,
  dirty,
  error,
  pending,
  onYamlChange,
  onDiscard,
  onSave,
}: {
  orgId: string
  projectId: string
  profile: AgentProfile
  canManage: boolean
  yaml: string
  dirty: boolean
  error: string
  pending: boolean
  onYamlChange: (value: string) => void
  onDiscard: () => void
  onSave: () => void
}) {
  return (
    <div className="flex flex-col gap-6">
      <DetailList
        items={[
          { label: 'Generation', value: profile.current_generation },
          {
            label: 'Model',
            value: `${profile.current_config.model.provider_config} · ${profile.current_config.model.name}`,
          },
          { label: 'Created', value: formatDateTime(profile.created_at) },
          { label: 'Updated', value: formatDateTime(profile.updated_at) },
        ]}
      />
      <form
        onSubmit={(event) => {
          event.preventDefault()
          onSave()
        }}
      >
        <FieldGroup>
          <AgentConfigYamlField
            id="agent-profile-config-yaml"
            value={yaml}
            onChange={onYamlChange}
            readOnly={!canManage}
            className="h-[28rem]"
          />
          {profile.current_config.source === undefined && yaml.trim() === '' && (
            <p className="text-muted-foreground text-sm">
              The current source is unavailable. Paste the replacement YAML configuration.
            </p>
          )}
          {error && <p className="text-destructive whitespace-pre-wrap text-sm">{error}</p>}
          {canManage && (
            <div className="flex items-center gap-2">
              <Button
                type="submit"
                disabled={pending || !dirty || yaml.trim() === ''}
                loading={pending}
              >
                Save revision
              </Button>
              {dirty && !pending && (
                <Button type="button" variant="ghost" onClick={onDiscard}>
                  Discard changes
                </Button>
              )}
            </div>
          )}
        </FieldGroup>
      </form>
      <AgentProfileIntegrations
        orgId={orgId}
        projectId={projectId}
        profileId={profile.id}
        canManage={canManage}
      />
    </div>
  )
}
