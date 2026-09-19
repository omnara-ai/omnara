import type { ProfileAppProvider, ProjectApp } from '@omnara/sdk'
import { Link, useNavigate, useParams, useSearch } from '@tanstack/react-router'
import { useState } from 'react'

import { AppCatalog } from '@/components/apps/AppCatalog'
import { appCatalog } from '@/components/apps/appDefinitions'
import { ConnectSlackDialog } from '@/components/apps/ConnectSlackDialog'
import { ProjectAppForm } from '@/components/apps/ProjectAppForm'
import { SlackOAuthOutcomeDialog } from '@/components/apps/SlackOAuthOutcomeDialog'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'

export function CreateProjectAppPage() {
  const { provider } = useParams({ strict: false })
  const selected = appCatalog.find((app) => app.provider === provider)
  return (
    <ProjectPageFrame title={selected ? `Set up ${selected.name}` : 'Add app'}>
      {({ activeOrg, projectId, project }) => {
        if (!project?.access.can_manage)
          return <p role="alert">You don’t have permission to manage apps in this project.</p>
        if (provider && !selected) return <p role="alert">App not found.</p>
        return selected ? (
          <AppSetup
            key={`${projectId}:${selected.provider}`}
            orgId={activeOrg.id}
            projectId={projectId}
            provider={selected.provider}
          />
        ) : (
          <>
            <header className="flex flex-col gap-2">
              <h1 className="type-title">Add app</h1>
              <p className="text-muted-foreground text-sm">
                Choose an app, then connect your account and configure its behavior.
              </p>
            </header>
            <AppCatalog projectId={projectId} />
          </>
        )
      }}
    </ProjectPageFrame>
  )
}

function AppSetup({
  orgId,
  projectId,
  provider,
}: {
  orgId: string
  projectId: string
  provider: ProfileAppProvider
}) {
  const [connecting, setConnecting] = useState(false)
  const search = useSearch({
    from: '/authenticated/onboarded/projects/$projectId/apps/new/$provider',
  })
  const navigate = useNavigate()
  const selected = appCatalog.find((app) => app.provider === provider)
  function saved(app: ProjectApp) {
    void navigate({ to: '/projects/$projectId/apps/$appId', params: { projectId, appId: app.id } })
  }
  return (
    <div className="flex max-w-2xl flex-col gap-6">
      <header className="flex flex-col gap-2">
        <Link
          className="text-muted-foreground text-sm hover:underline"
          to="/projects/$projectId/apps/new"
          params={{ projectId }}
        >
          Choose another app
        </Link>
        <h1 className="type-title">Set up {selected?.name}</h1>
        <p className="text-muted-foreground text-sm">{selected?.description}</p>
      </header>
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        provider={provider}
        initialConnectionId={search.integration_connection}
        onSaved={saved}
        onConnectSlack={() => {
          setConnecting(true)
        }}
      />
      {connecting && (
        <ConnectSlackDialog open onOpenChange={setConnecting} orgId={orgId} projectId={projectId} />
      )}
      <SlackOAuthOutcomeDialog />
    </div>
  )
}
