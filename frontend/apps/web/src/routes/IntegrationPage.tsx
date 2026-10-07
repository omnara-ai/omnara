import { useIntegration } from '@omnara/react'
import { ApiError, type Integration } from '@omnara/sdk'
import { Link, useParams } from '@tanstack/react-router'
import { type ReactNode, useCallback, useState } from 'react'

import { PillTabs } from '@/components/agents/PillTabs'
import { IntegrationAdvanced } from '@/components/integrations/IntegrationAdvanced'
import { IntegrationConversations } from '@/components/integrations/IntegrationConversations'
import { integrationCatalog } from '@/components/integrations/integrationDefinitions'
import { IntegrationDetailLayout } from '@/components/integrations/IntegrationDetailLayout'
import { IntegrationLaunch } from '@/components/integrations/IntegrationLaunch'
import { IntegrationSchedules } from '@/components/integrations/IntegrationSchedules'
import {
  type SlackOAuthOutcome,
  useSlackOAuthOutcome,
} from '@/components/integrations/useSlackOAuthOutcome'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useActiveOrg } from '@/lib/use-active-org'

export function IntegrationPage() {
  const { projectId = '', integrationId = '' } = useParams({ strict: false })
  const { activeOrg } = useActiveOrg()
  const query = useIntegration(activeOrg.id, projectId, integrationId)
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId, project }) => (
        <>
          <PageBreadcrumb
            items={[
              {
                id: 'integrations',
                label: 'Integrations',
                to: '/projects/$projectId/integrations',
                params: { projectId },
              },
              { id: 'integration', label: query.data?.name ?? 'Integration' },
            ]}
          />
          <IntegrationDetail
            key={`${projectId}:${integrationId}`}
            orgId={activeOrg.id}
            projectId={projectId}
            integrationId={integrationId}
            canManage={project?.access.can_manage ?? false}
          />
        </>
      )}
    </ProjectPageFrame>
  )
}

export function IntegrationDetail({
  orgId,
  projectId,
  integrationId,
  canManage,
}: {
  orgId: string
  projectId: string
  integrationId: string
  canManage: boolean
}) {
  const { outcome: oauth, clear: clearOAuth } = useSlackOAuthOutcome(integrationId)
  const query = useIntegration(orgId, projectId, integrationId)
  if (query.isPending) return <Spinner className="size-4" />
  const unavailable =
    query.error instanceof ApiError && [401, 403, 404].includes(query.error.status)
  if (!query.data || unavailable)
    return (
      <div role="alert" className="flex flex-col gap-3">
        <p>
          Could not load this integration. It may have been removed, or you may not have access.
        </p>
        {oauth?.kind === 'error' && <p>{oauth.description}</p>}
        <Button className="self-start" variant="outline" onClick={() => void query.refetch()}>
          Retry
        </Button>
        <Link to="/projects/$projectId/integrations" params={{ projectId }}>
          Back to integrations
        </Link>
      </div>
    )
  return (
    <IntegrationSettings
      orgId={orgId}
      projectId={projectId}
      integration={query.data}
      oauth={oauth}
      onOAuthCleared={clearOAuth}
      canManage={canManage}
      refreshFailed={query.isError}
      onRefresh={() => void query.refetch()}
    />
  )
}

function IntegrationSettings({
  orgId,
  projectId,
  integration,
  canManage,
  refreshFailed,
  onRefresh,
  oauth,
  onOAuthCleared,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canManage: boolean
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
  onOAuthCleared: () => void
}) {
  const integrationKind = integration.integration_kind
  const canSetUp =
    canManage &&
    integrationCatalog.some((definition) => definition.integrationKind === integrationKind)
  const draft = integration.state === 'disconnected' && !integration.provider_tenant_id
  const [initialSetup, setInitialSetup] = useState(draft)
  const [connected, setConnected] = useState(oauth?.kind === 'success')
  const finishConnection = useCallback(() => {
    setConnected(true)
    onOAuthCleared()
  }, [onOAuthCleared])
  return (
    <IntegrationDetailLayout
      orgId={orgId}
      projectId={projectId}
      integration={integration}
      canManage={canManage}
      canSetUp={canSetUp}
      draft={draft}
      connected={connected}
      onConnected={finishConnection}
      refreshFailed={refreshFailed}
      onRefresh={onRefresh}
      oauth={oauth}
    >
      {(removeAction) =>
        draft ? (
          <>
            <DraftIntegrationSections
              orgId={orgId}
              projectId={projectId}
              integration={integration}
              canManage={canManage}
            />
            {removeAction}
          </>
        ) : (
          <IntegrationTabs
            removeAction={removeAction}
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            canManage={canManage}
            canSetUp={canSetUp}
            initialSetup={initialSetup}
            onSetupFinished={() => {
              setConnected(false)
              setInitialSetup(false)
            }}
          />
        )
      }
    </IntegrationDetailLayout>
  )
}

type IntegrationTab = 'launch' | 'schedules' | 'conversations' | 'advanced'

/** A connected integration's settings, one concern per tab. */
function IntegrationTabs({
  orgId,
  projectId,
  integration,
  canManage,
  canSetUp,
  initialSetup,
  onSetupFinished,
  removeAction,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canManage: boolean
  canSetUp: boolean
  initialSetup: boolean
  onSetupFinished: () => void
  /** Delete lives with the other rarely-needed settings. */
  removeAction: ReactNode
}) {
  const [tab, setTab] = useState<IntegrationTab>('launch')
  const tabs: { value: IntegrationTab; label: string }[] = [
    {
      value: 'launch',
      label: integration.integration_kind === 'github_pr' ? 'Pull requests' : 'Mentions',
    },
    ...(integration.capabilities.schedule
      ? [{ value: 'schedules' as const, label: 'Schedules' }]
      : []),
    { value: 'conversations', label: 'Conversations' },
    { value: 'advanced', label: 'Advanced' },
  ]
  return (
    <div className="flex flex-col gap-6">
      <div role="group" aria-label="Integration settings">
        <PillTabs value={tab} onValueChange={setTab} tabs={tabs} />
      </div>
      {/* Every tab stays mounted so unsaved edits survive switching tabs. */}
      <div hidden={tab !== 'launch'}>
        <IntegrationLaunch
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canEdit={canSetUp}
          initialSetup={initialSetup}
          onSetupFinished={onSetupFinished}
        />
      </div>
      {integration.capabilities.schedule && (
        <div hidden={tab !== 'schedules'}>
          <IntegrationSchedules
            orgId={orgId}
            projectId={projectId}
            integration={integration}
            canManage={canManage}
            titled={false}
          />
        </div>
      )}
      <div hidden={tab !== 'conversations'}>
        <IntegrationConversations
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canManage={canManage}
          titled={false}
        />
      </div>
      <div hidden={tab !== 'advanced'} className="flex flex-col gap-8">
        <IntegrationAdvanced integration={integration} />
        {removeAction}
      </div>
    </div>
  )
}

/** Before the first connection there's nothing to tab between; show only existing activity. */
function DraftIntegrationSections({
  orgId,
  projectId,
  integration,
  canManage,
}: {
  orgId: string
  projectId: string
  integration: Integration
  canManage: boolean
}) {
  return (
    <>
      {integration.capabilities.schedule && (
        <IntegrationSchedules
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canManage={canManage}
          hideWhenEmpty
        />
      )}
      <IntegrationConversations
        orgId={orgId}
        projectId={projectId}
        integration={integration}
        canManage={canManage}
        hideWhenEmpty
      />
    </>
  )
}
