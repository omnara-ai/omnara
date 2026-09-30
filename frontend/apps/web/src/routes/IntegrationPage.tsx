import { useIntegration } from '@omnara/react'
import { ApiError, type Integration } from '@omnara/sdk'
import { Link, useParams } from '@tanstack/react-router'
import { useCallback, useState } from 'react'

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
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function IntegrationPage() {
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
        <IntegrationDetail
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
  const [editing, setEditing] = useState(
    canSetUp && oauth?.kind === 'success' && !integration.settings.launcher,
  )
  const finishConnection = useCallback(
    (savedIntegration: Integration) => {
      setConnected(true)
      if (canSetUp && !savedIntegration.settings.launcher) setEditing(true)
      onOAuthCleared()
    },
    [canSetUp, onOAuthCleared],
  )
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
      {!draft && (
        <IntegrationLaunch
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canEdit={canSetUp}
          editing={editing}
          initialSetup={connected && initialSetup}
          onEditingChange={(next) => {
            setEditing(next)
            if (!next) {
              setConnected(false)
              setInitialSetup(false)
            }
          }}
        />
      )}
      {integration.capabilities.schedule && (
        <IntegrationSchedules
          orgId={orgId}
          projectId={projectId}
          integration={integration}
          canManage={canManage}
          hideWhenEmpty={draft}
        />
      )}
      <IntegrationConversations
        orgId={orgId}
        projectId={projectId}
        integration={integration}
        canManage={canManage}
        hideWhenEmpty={draft}
      />
      {!draft && <IntegrationAdvanced integration={integration} />}
    </IntegrationDetailLayout>
  )
}
