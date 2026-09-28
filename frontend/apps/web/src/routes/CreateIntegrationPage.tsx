import { useIntegration,useIntegrationDefinitions } from '@omnara/react'
import type { Integration,IntegrationKind } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { IntegrationCatalog } from '@/components/integrations/IntegrationCatalog'
import { IntegrationConnection } from '@/components/integrations/IntegrationConnection'
import { integrationCatalog } from '@/components/integrations/integrationDefinitions'
import { IntegrationForm } from '@/components/integrations/IntegrationForm'
import { IntegrationIcon } from '@/components/integrations/IntegrationIcon'
import { IntegrationPortalSetup } from '@/components/integrations/IntegrationPortalSetup'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function CreateIntegrationPage() {
  const { projectId = '', integrationKind } = useParams({ strict: false })
  const selected = integrationCatalog.find(
    (integration) => integration.integrationKind === integrationKind,
  )
  return (
    <ProjectPageFrame
      title={selected ? selected.name : 'Add integration'}
      breadcrumbs={[
        {
          id: 'integrations',
          label: 'Integrations',
          to: '/projects/$projectId/integrations',
          params: { projectId },
        },
        ...(selected
          ? [
              {
                id: 'catalog',
                label: 'Add integration',
                to: '/projects/$projectId/integrations/new' as const,
                params: { projectId },
              },
            ]
          : []),
      ]}
    >
      {({ activeOrg, projectId, project }) => {
        if (!project?.access.can_manage)
          return (
            <p role="alert">You don’t have permission to manage integrations in this project.</p>
          )
        if (integrationKind && !selected) return <p role="alert">Integration not found.</p>
        return selected ? (
          <IntegrationCreateSetup
            key={`${projectId}:${selected.integrationKind}`}
            orgId={activeOrg.id}
            projectId={projectId}
            integrationKind={selected.integrationKind}
          />
        ) : (
          <>
            <header className="flex flex-col gap-2">
              <h1 className="type-title">Add integration</h1>
              <p className="text-muted-foreground text-sm">
                Choose an integration for your agents.
              </p>
            </header>
            <IntegrationCatalog orgId={activeOrg.id} projectId={projectId} />
          </>
        )
      }}
    </ProjectPageFrame>
  )
}

export function IntegrationCreateSetup({
  orgId,
  projectId,
  integrationKind,
}: {
  orgId: string
  projectId: string
  integrationKind: IntegrationKind
}) {
  const query = useIntegrationDefinitions(orgId, projectId)
  const navigate = useNavigate()
  const [connected, setConnected] = useState<Integration>()
  const connectedQuery = useIntegration(orgId, projectId, connected?.id ?? '')
  const savedIntegration = connectedQuery.data ?? connected
  const openIntegration = (integration: Integration) =>
    void navigate({
      to: '/projects/$projectId/integrations/$integrationId',
      replace: true,
      params: { projectId, integrationId: integration.id },
    })
  const selected = integrationCatalog.find(
    (integration) => integration.integrationKind === integrationKind,
  )
  if (query.isPending) return <Spinner className="size-4" />
  if (!query.data)
    return (
      <div role="alert">
        Could not load integration definition.{' '}
        <Button onClick={() => void query.refetch()}>Retry</Button>
      </div>
    )
  if (!query.data.data.some((integration) => integration.integration_kind === integrationKind))
    return <p role="alert">This integration is unavailable.</p>
  return (
    <div className="flex w-full max-w-2xl flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Link
          className="text-muted-foreground text-sm hover:underline"
          to="/projects/$projectId/integrations/new"
          params={{ projectId }}
        >
          Choose another integration
        </Link>
        <div className="flex items-center gap-3">
          <IntegrationIcon integrationKind={integrationKind} className="size-7" />
          <h1 className="type-title">Add {selected?.name}</h1>
        </div>
        <p className="text-muted-foreground text-sm">{selected?.description}</p>
      </header>
      {query.isError && (
        <div role="alert" className="text-sm">
          Could not refresh the integration definition. Your setup is kept.{' '}
          <Button variant="link" onClick={() => void query.refetch()}>
            Retry refresh
          </Button>
        </div>
      )}
      {savedIntegration ? (
        <section
          className="flex flex-col gap-5"
          aria-label={integrationKind === 'github_pr' ? 'Pull requests' : 'Mentions'}
        >
          <p role="status" className="text-sm">
            {integrationKind === 'discord_thread'
              ? 'Account connected. Finish setup in Discord below, then set up mentions. You can add schedules on the integration page.'
              : 'Account connected. Choose which agents this integration can start. Connection details remain available on the integration page.'}
          </p>
          {savedIntegration.integration_kind === 'discord_thread' && (
            <IntegrationPortalSetup
              integrationKind="discord_thread"
              providerId={savedIntegration.provider_tenant_id}
              title="3. Finish in Discord"
            />
          )}
          {savedIntegration.integration_kind === 'discord_thread' && (
            <h2 className="pt-3 text-sm font-medium">4. Set up mentions</h2>
          )}
          <IntegrationForm
            orgId={orgId}
            projectId={projectId}
            integrationKind={integrationKind}
            integration={savedIntegration}
            onSaved={openIntegration}
            onCancel={() => {
              openIntegration(savedIntegration)
            }}
            cancelLabel="Skip for now"
          />
        </section>
      ) : (
        <IntegrationConnection
          orgId={orgId}
          projectId={projectId}
          integrationKind={integrationKind}
          onConnected={setConnected}
        />
      )}
    </div>
  )
}
