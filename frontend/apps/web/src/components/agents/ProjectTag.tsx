import type { VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'

import { Folder } from '@/components/icons'

export function ProjectTag({ project }: { project: VisibleProject }) {
  return (
    <Link
      to="/projects/$projectId"
      params={{ projectId: project.id }}
      className="text-muted-foreground hover:text-foreground relative inline-flex min-w-0 shrink items-center gap-1 text-xs hover:underline"
    >
      <Folder className="size-3.5 shrink-0" />
      <span className="truncate">{project.name}</span>
    </Link>
  )
}
