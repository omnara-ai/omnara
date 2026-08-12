import { Link } from '@tanstack/react-router'
import { BotIcon } from 'lucide-react'

import { generalAgentTemplateId } from '@/components/agents/agentTemplates'
import { Button } from '@/components/ui/button'

export function FirstAgentCard({ projectId }: { projectId: string }) {
  return (
    <div className="flex flex-col gap-6 rounded-xl border p-6 sm:flex-row sm:items-center sm:justify-between">
      <div className="flex items-start gap-4">
        <div className="bg-muted flex size-10 shrink-0 items-center justify-center rounded-lg">
          <BotIcon className="size-5" />
        </div>
        <div className="flex flex-col gap-1">
          <h2 className="text-base font-semibold tracking-tight">Create your first agent</h2>
          <p className="text-muted-foreground text-sm">
            Start from a template, launch it, and give it its first task.
          </p>
          <p className="text-muted-foreground mt-1 text-xs">
            Prefer code? See the{' '}
            <a
              href="https://docs.omnara.com/api/overview"
              target="_blank"
              rel="noreferrer"
              className="hover:text-foreground underline underline-offset-2"
            >
              API docs
            </a>
            .
          </p>
        </div>
      </div>
      <Button asChild className="w-fit shrink-0">
        <Link
          to="/projects/$projectId/agents/new"
          params={{ projectId }}
          search={{ template: generalAgentTemplateId }}
        >
          New agent
        </Link>
      </Button>
    </div>
  )
}
