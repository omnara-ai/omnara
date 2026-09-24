import { useProjectIntegration } from '@omnara/react'
import { ApiError, type ProjectIntegration } from '@omnara/sdk'
import { Link, useParams } from '@tanstack/react-router'
import { useCallback, useState } from 'react'

import { integrationCatalog } from '@/components/integrations/integrationDefinitions'
import { ProjectIntegrationAdvanced } from '@/components/integrations/ProjectIntegrationAdvanced'
import { ProjectIntegrationConversations } from '@/components/integrations/ProjectIntegrationConversations'
import { ProjectIntegrationDetailLayout } from '@/components/integrations/ProjectIntegrationDetailLayout'
import { ProjectIntegrationLaunch } from '@/components/integrations/ProjectIntegrationLaunch'
import { ProjectIntegrationSchedules } from '@/components/integrations/ProjectIntegrationSchedules'
import {
  type SlackOAuthOutcome,
  useSlackOAuthOutcome,
} from '@/components/integrations/useSlackOAuthOutcome'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function ProjectIntegrationPage() {
  const { projectId = '', integrationId = '' } = useParams({ strict: false })
  return (
    <ProjectPageFrame
      title="Integration"
      breadcrumbs={[
        {
          id: 'integrations',
          label: 'Integrations',
          to: '/projects/$projectId/integrations',
          params: { projectId },
        },
      ]}
    >
      {({ activeOrg, projectId, project }) => (
        <ProjectIntegrationDetail
          key={`${projectId}:${integrationId}`}
          orgId={activeOrg.id}
          projectId={projectId}
          integrationId={integrationId}
          canManage={project?.access.can_manage ?? false}
        />
      )}
    </ProjectPageFrame>
  )
}

export function ProjectIntegrationDetail({
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
  const query = useProjectIntegration(orgId, projectId, integrationId)
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
    <ProjectIntegrationSettings
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

function ProjectIntegrationSettings({
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
  integration: ProjectIntegration
  canManage: boolean
  refreshFailed: boolean
  onRefresh: () => void
  oauth: SlackOAuthOutcome | null
  onOAuthCleared: () => void
}) {
  const integrationType = integration.integration_type
  const canSetUp =
    canManage &&
    integrationCatalog.some((definition) => definition.integrationType === integrationType)
  const draft = integration.state === 'disconnected' && !integration.provider_tenant_id
  const [connected, setConnected] = useState(oauth?.kind === 'success')
  const [editing, setEditing] = useState(
    canSetUp && oauth?.kind === 'success' && !integration.settings.launcher,
  )
  const finishConnection = useCallback(
    (savedIntegration: ProjectIntegration) => {
      setConnected(true)
      if (canSetUp && !savedIntegration.settings.launcher) setEditing(true)
      onOAuthCleared()
    },
    [canSetUp, onOAuthCleared],
  )
  return (
    <ProjectIntegrationDetailLayout
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
      {!draft && (
        <ProjectIntegrationLaunch
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canEdit={canSetUp}
          editing={editing}
          onEditingChange={(next) => {
            setEditing(next)
            if (!next) setConnected(false)
          }}
        />
      )}
      {integration.capabilities.schedule && (
        <ProjectIntegrationSchedules
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canManage={canManage}
          hideWhenEmpty={draft}
        />
      )}
      <ProjectIntegrationConversations
        orgId={orgId}
        projectId={projectId}
        integration={integration}
        canManage={canManage}
        hideWhenEmpty={draft}
      />
      {!draft && <ProjectIntegrationAdvanced integration={integration} />}
    </ProjectIntegrationDetailLayout>
  )
}
