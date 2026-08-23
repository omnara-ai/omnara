import { Link } from '@tanstack/react-router'

import { generalAgentTemplateId } from '@/components/agents/agentTemplates'
import { BotIcon, PlusIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'

export function FirstAgentCard({ projectId }: { projectId: string }) {
  return (
    <div className="flex max-w-md flex-col items-center text-center">
      <div className="bg-accent text-primary flex size-10 items-center justify-center rounded-lg">
        <BotIcon className="size-5" />
      </div>
      <h2 className="mt-4 text-lg font-semibold tracking-tight">Create your first agent</h2>
      <p className="text-muted-foreground mt-1.5 text-pretty text-sm leading-relaxed">
        Start from a template, launch it, and give it its first task. Prefer code? See the{' '}
        <a
          href="https://docs.omnara.com/api/overview"
          target="_blank"
          rel="noreferrer"
          className="text-primary hover:underline"
        >
          API docs
        </a>
        .
      </p>
      <Button asChild className="mt-5">
        <Link
          to="/projects/$projectId/agents/new"
          params={{ projectId }}
          search={{ template: generalAgentTemplateId }}
        >
          <PlusIcon />
          New agent
        </Link>
      </Button>
    </div>
  )
}
