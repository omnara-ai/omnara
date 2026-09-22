import { useAppDefinitions, useProjectApp } from '@omnara/react'
import type { AppType, ProjectApp } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { AppCatalog } from '@/components/apps/AppCatalog'
import { appCatalog } from '@/components/apps/appDefinitions'
import { AppIcon } from '@/components/apps/AppIcon'
import { ConnectGitHubForm } from '@/components/apps/ConnectGitHubForm'
import { ConnectSlackForm } from '@/components/apps/ConnectSlackForm'
import { ProjectAppForm } from '@/components/apps/ProjectAppForm'
import { ProjectAppPortalSetup } from '@/components/apps/ProjectAppPortalSetup'
import { ProjectAppSetup } from '@/components/apps/ProjectAppSetup'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function CreateProjectAppPage() {
  const { projectId = '', appType } = useParams({ strict: false })
  const selected = appCatalog.find((app) => app.appType === appType)
  return (
    <ProjectPageFrame
      title={selected ? selected.name : 'Add app'}
      breadcrumbs={[
        { id: 'apps', label: 'Apps', to: '/projects/$projectId/apps', params: { projectId } },
        ...(selected
          ? [
              {
                id: 'catalog',
                label: 'Add app',
                to: '/projects/$projectId/apps/new' as const,
                params: { projectId },
              },
            ]
          : []),
      ]}
    >
      {({ activeOrg, projectId, project }) => {
        if (!project?.access.can_manage)
          return <p role="alert">You don’t have permission to manage apps in this project.</p>
        if (appType && !selected) return <p role="alert">App not found.</p>
        return selected ? (
          <ProjectAppCreateSetup
            key={`${projectId}:${selected.appType}`}
            orgId={activeOrg.id}
            projectId={projectId}
            appType={selected.appType}
          />
        ) : (
          <>
            <header className="flex flex-col gap-2">
              <h1 className="type-title">Add app</h1>
              <p className="text-muted-foreground text-sm">
                Choose an app to connect to your agents.
              </p>
            </header>
            <AppCatalog orgId={activeOrg.id} projectId={projectId} />
          </>
        )
      }}
    </ProjectPageFrame>
  )
}

export function ProjectAppCreateSetup({
  orgId,
  projectId,
  appType,
}: {
  orgId: string
  projectId: string
  appType: AppType
}) {
  const query = useAppDefinitions(orgId, projectId)
  const navigate = useNavigate()
  const [connected, setConnected] = useState<ProjectApp>()
  const connectedQuery = useProjectApp(orgId, projectId, connected?.id ?? '')
  const savedApp = connectedQuery.data ?? connected
  const openApp = (app: ProjectApp) =>
    void navigate({
      to: '/projects/$projectId/apps/$appId',
      replace: true,
      params: { projectId, appId: app.id },
    })
  const selected = appCatalog.find((app) => app.appType === appType)
  if (query.isPending) return <Spinner className="size-4" />
  if (!query.data)
    return (
      <div role="alert">
        Could not load app definition. <Button onClick={() => void query.refetch()}>Retry</Button>
      </div>
    )
  if (!query.data.data.some((app) => app.app_type === appType))
    return <p role="alert">This app is unavailable.</p>
  return (
    <div className="flex w-full max-w-2xl flex-col gap-8">
      <header className="flex flex-col gap-2">
        <Link
          className="text-muted-foreground text-sm hover:underline"
          to="/projects/$projectId/apps/new"
          params={{ projectId }}
        >
          Choose another app
        </Link>
        <div className="flex items-center gap-3">
          <AppIcon appType={appType} className="size-7" />
          <h1 className="type-title">Add {selected?.name}</h1>
        </div>
        <p className="text-muted-foreground text-sm">{selected?.description}</p>
      </header>
      {query.isError && (
        <div role="alert" className="text-sm">
          Could not refresh the app definition. Your setup is kept.{' '}
          <Button variant="link" onClick={() => void query.refetch()}>
            Retry refresh
          </Button>
        </div>
      )}
      {savedApp ? (
        <section
          className="flex flex-col gap-5"
          aria-label={appType === 'github_pr' ? 'Pull requests' : 'Mentions'}
        >
          <p role="status" className="text-sm">
            {appType === 'discord_thread'
              ? 'Account connected. Finish setup in Discord below, then set up mentions. You can add schedules on the app page.'
              : 'Account connected. Choose which agents this app can start. Connection details remain available on the app page.'}
          </p>
          {savedApp.app_type === 'discord_thread' && (
            <ProjectAppPortalSetup
              appType="discord_thread"
              providerId={savedApp.provider_tenant_id}
              title="3. Finish in Discord"
            />
          )}
          {savedApp.app_type === 'discord_thread' && (
            <h2 className="pt-3 text-sm font-medium">4. Set up mentions</h2>
          )}
          <ProjectAppForm
            orgId={orgId}
            projectId={projectId}
            appType={appType}
            app={savedApp}
            onSaved={openApp}
            onCancel={() => {
              openApp(savedApp)
            }}
            cancelLabel="Skip for now"
          />
        </section>
      ) : appType === 'slack_thread' ? (
        <ConnectSlackForm orgId={orgId} projectId={projectId} onConnected={setConnected} />
      ) : appType === 'github_pr' ? (
        <ConnectGitHubForm orgId={orgId} projectId={projectId} onConnected={setConnected} />
      ) : (
        <ProjectAppSetup
          orgId={orgId}
          projectId={projectId}
          appType={appType}
          onSaved={setConnected}
        />
      )}
    </div>
  )
}
