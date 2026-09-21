import { useAppDefinitions } from '@omnara/react'
import type { AppType } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'

import { AppCatalog } from '@/components/apps/AppCatalog'
import { appCatalog } from '@/components/apps/appDefinitions'
import { ProjectAppForm } from '@/components/apps/ProjectAppForm'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

export function CreateProjectAppPage() {
  const { appType } = useParams({ strict: false })
  const selected = appCatalog.find((app) => app.appType === appType)
  return (
    <ProjectPageFrame title={selected ? `Add ${selected.name}` : 'Add app'}>
      {({ activeOrg, projectId, project }) => {
        if (!project?.access.can_manage)
          return <p role="alert">You don’t have permission to manage apps in this project.</p>
        if (appType && !selected) return <p role="alert">App not found.</p>
        return selected ? (
          <AppSetup
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
                Choose an app, name it, then connect your account.
              </p>
            </header>
            <AppCatalog orgId={activeOrg.id} projectId={projectId} />
          </>
        )
      }}
    </ProjectPageFrame>
  )
}

function AppSetup({
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
  const selected = appCatalog.find((app) => app.appType === appType)
  if (query.isPending) return <Spinner className="size-4" />
  if (query.isError)
    return (
      <div role="alert">
        Could not load app definition. <Button onClick={() => void query.refetch()}>Retry</Button>
      </div>
    )
  if (!query.data.data.some((app) => app.app_type === appType))
    return <p role="alert">This app is unavailable.</p>
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
        <h1 className="type-title">Add {selected?.name}</h1>
        <p className="text-muted-foreground text-sm">{selected?.description}</p>
      </header>
      <ProjectAppForm
        orgId={orgId}
        projectId={projectId}
        appType={appType}
        onSaved={(app) =>
          void navigate({
            to: '/projects/$projectId/apps/$appId',
            params: { projectId, appId: app.id },
          })
        }
      />
    </div>
  )
}
