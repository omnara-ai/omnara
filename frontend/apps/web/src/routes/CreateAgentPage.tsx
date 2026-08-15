import { CreateAgentForm } from '@/components/agents/CreateAgentForm'
import { FullPageSpinner } from '@/components/ui/spinner'
import { useProjectPage } from '@/lib/use-project-page'

export function CreateAgentPage() {
  const { project, isPending } = useProjectPage()

  if (isPending) return <FullPageSpinner />

  if (!project?.access.can_manage) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">
          {project ? 'Not allowed' : 'Project not found'}
        </h1>
        <p className="text-muted-foreground text-sm">
          {project
            ? 'You don’t have permission to create agents in this project.'
            : 'This project doesn’t exist or you don’t have access to it.'}
        </p>
      </div>
    )
  }

  return <CreateAgentForm />
}
