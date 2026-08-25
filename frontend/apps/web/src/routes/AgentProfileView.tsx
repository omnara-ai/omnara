import { useAgentProfile, useCreateAgent, useDeleteAgentProfile } from '@omnara/react'
import { type AgentProfile, ApiError } from '@omnara/sdk'
import { useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import type { AgentConfigMode } from '@/components/agents/agentConfigModeMachine'
import { AgentProfileConfigEditor } from '@/components/agents/AgentProfileConfigEditor'
import { AgentProfileIntegrations } from '@/components/agents/AgentProfileIntegrations'
import { AgentProfileNameHeading } from '@/components/agents/AgentProfileNameHeading'
import { AgentsTable } from '@/components/agents/AgentsSection'
import { CreateCronTriggerDialog } from '@/components/agents/CronTriggerDialog'
import { CronTriggersList } from '@/components/agents/CronTriggersSection'
import { DeployAgentProfileDialog } from '@/components/agents/DeployAgentProfileDialog'
import { PillTabs } from '@/components/agents/PillTabs'
import { SlackOAuthOutcomeDialog } from '@/components/agents/SlackOAuthOutcomeDialog'
import { DetailList } from '@/components/data-table/DetailList'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { Button } from '@/components/ui/button'
import { formatDateTime } from '@/lib/format'
import { useActiveOrg } from '@/lib/use-active-org'
import { useProjectPage } from '@/lib/use-project-page'

type ProfileTab = 'configuration' | 'integrations' | 'schedules' | 'agents'

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
  const [addCronOpen, setAddCronOpen] = useState(false)
  const [configDirty, setConfigDirty] = useState(false)

  const createAgent = useCreateAgent(activeOrg.id, projectId)
  const deleteProfile = useDeleteAgentProfile(activeOrg.id, projectId)
  const navigate = useNavigate()

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
            <AgentProfileNameHeading
              orgId={activeOrg.id}
              projectId={projectId}
              profile={profile}
              canManage={canManage}
            />
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
          </div>
        </div>
        <PillTabs
          value={tab}
          onValueChange={setTab}
          tabs={[
            { value: 'configuration', label: 'Configuration' },
            { value: 'integrations', label: 'Integrations' },
            { value: 'schedules', label: 'Schedules' },
            { value: 'agents', label: 'Agents' },
          ]}
        />
      </header>

      <div className={tab === 'configuration' ? 'contents' : 'hidden'}>
        <ConfigurationTab
          orgId={activeOrg.id}
          projectId={projectId}
          profile={profile}
          canManage={canManage}
          onDirtyChange={setConfigDirty}
          onDelete={remove}
        />
      </div>
      {tab === 'integrations' && (
        <div className="flex flex-col gap-4">
          {canManage && (
            <div className="flex justify-end">
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setDeployOpen(true)
                }}
              >
                Add integration
              </Button>
            </div>
          )}
          <AgentProfileIntegrations
            orgId={activeOrg.id}
            projectId={projectId}
            profileId={profile.id}
            canManage={canManage}
          />
        </div>
      )}
      {tab === 'schedules' && (
        <div className="flex flex-col gap-4">
          <div className="flex items-center justify-between gap-3">
            <p className="text-muted-foreground text-sm">Launch new agents on a schedule</p>
            {canManage && (
              <Button
                size="sm"
                variant="outline"
                onClick={() => {
                  setAddCronOpen(true)
                }}
              >
                Add cron schedule
              </Button>
            )}
          </div>
          <CronTriggersList
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={canManage}
            filters={{ agent_profile_id: profile.id }}
            emptyMessage={
              canManage
                ? 'No schedules yet. Use “Add cron schedule” to launch a new agent from this profile on a recurring cadence.'
                : 'No schedules yet.'
            }
          />
        </div>
      )}
      {tab === 'agents' && (
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
      {canManage && addCronOpen && (
        <CreateCronTriggerDialog
          open
          onOpenChange={setAddCronOpen}
          orgId={activeOrg.id}
          projectId={projectId}
          target={{ type: 'profile', agent_profile_id: profile.id }}
          targetLabel={profile.name}
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
  onDirtyChange,
  onDelete,
}: {
  orgId: string
  projectId: string
  profile: AgentProfile
  canManage: boolean
  onDirtyChange: (dirty: boolean) => void
  onDelete: () => void
}) {
  const [resetNonce, setResetNonce] = useState(0)
  const [preferredMode, setPreferredMode] = useState<AgentConfigMode>('builder')

  return (
    <div className="flex flex-col gap-6">
      <DetailList
        items={[
          { label: 'ID', value: profile.id, mono: true },
          { label: 'Generation', value: profile.current_generation },
          {
            label: 'Model',
            value: `${profile.current_config.model.provider_config} · ${profile.current_config.model.name}`,
          },
          { label: 'Created', value: formatDateTime(profile.created_at) },
          { label: 'Updated', value: formatDateTime(profile.updated_at) },
        ]}
      />
      <AgentProfileConfigEditor
        key={`${profile.current_config_id}:${resetNonce}`}
        orgId={orgId}
        projectId={projectId}
        profile={profile}
        canManage={canManage}
        preferredMode={preferredMode}
        onModeChange={setPreferredMode}
        onDirtyChange={onDirtyChange}
        onDiscard={() => {
          setResetNonce((nonce) => nonce + 1)
        }}
        onDelete={onDelete}
      />
    </div>
  )
}
