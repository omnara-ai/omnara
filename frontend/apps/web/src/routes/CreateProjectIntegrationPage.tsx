import { useIntegrationDefinitions, useProjectIntegration } from '@omnara/react'
import type { IntegrationType, ProjectIntegration } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { IntegrationCatalog } from '@/components/integrations/IntegrationCatalog'
import { integrationCatalog } from '@/components/integrations/integrationDefinitions'
import { IntegrationIcon } from '@/components/integrations/IntegrationIcon'
import { ProjectIntegrationConnection } from '@/components/integrations/ProjectIntegrationConnection'
import { ProjectIntegrationForm } from '@/components/integrations/ProjectIntegrationForm'
import { ProjectIntegrationPortalSetup } from '@/components/integrations/ProjectIntegrationPortalSetup'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function CreateProjectIntegrationPage() {
  const { projectId = '', integrationType } = useParams({ strict: false })
  const selected = integrationCatalog.find(
    (integration) => integration.integrationType === integrationType,
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
        if (integrationType && !selected) return <p role="alert">Integration not found.</p>
        return selected ? (
          <ProjectIntegrationCreateSetup
            key={`${projectId}:${selected.integrationType}`}
            orgId={activeOrg.id}
            projectId={projectId}
            integrationType={selected.integrationType}
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

export function ProjectIntegrationCreateSetup({
  orgId,
  projectId,
  integrationType,
}: {
  orgId: string
  projectId: string
  integrationType: IntegrationType
}) {
  const query = useIntegrationDefinitions(orgId, projectId)
  const navigate = useNavigate()
  const [connected, setConnected] = useState<ProjectIntegration>()
  const connectedQuery = useProjectIntegration(orgId, projectId, connected?.id ?? '')
  const savedIntegration = connectedQuery.data ?? connected
  const openIntegration = (integration: ProjectIntegration) =>
    void navigate({
      to: '/projects/$projectId/integrations/$integrationId',
      replace: true,
      params: { projectId, integrationId: integration.id },
    })
  const selected = integrationCatalog.find(
    (integration) => integration.integrationType === integrationType,
  )
  if (query.isPending) return <Spinner className="size-4" />
  if (!query.data)
    return (
      <div role="alert">
        Could not load integration definition.{' '}
        <Button onClick={() => void query.refetch()}>Retry</Button>
      </div>
    )
  if (!query.data.data.some((integration) => integration.integration_type === integrationType))
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
          <IntegrationIcon integrationType={integrationType} className="size-7" />
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
          aria-label={integrationType === 'github_pr' ? 'Pull requests' : 'Mentions'}
        >
          <p role="status" className="text-sm">
            {integrationType === 'discord_thread'
              ? 'Account connected. Finish setup in Discord below, then set up mentions. You can add schedules on the integration page.'
              : 'Account connected. Choose which agents this integration can start. Connection details remain available on the integration page.'}
          </p>
          {savedIntegration.integration_type === 'discord_thread' && (
            <ProjectIntegrationPortalSetup
              integrationType="discord_thread"
              providerId={savedIntegration.provider_tenant_id}
              title="3. Finish in Discord"
            />
          )}
          {savedIntegration.integration_type === 'discord_thread' && (
            <h2 className="pt-3 text-sm font-medium">4. Set up mentions</h2>
          )}
          <ProjectIntegrationForm
            orgId={orgId}
            projectId={projectId}
            integrationType={integrationType}
            integration={savedIntegration}
            onSaved={openIntegration}
            onCancel={() => {
              openIntegration(savedIntegration)
            }}
            cancelLabel="Skip for now"
          />
        </section>
      ) : (
        <ProjectIntegrationConnection
          orgId={orgId}
          projectId={projectId}
          integrationType={integrationType}
          onConnected={setConnected}
        />
      )}
    </div>
  )
}
