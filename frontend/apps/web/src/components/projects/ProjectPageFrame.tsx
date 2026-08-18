import type { ReactNode } from 'react'

import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { FullPageSpinner } from '@/components/ui/spinner'
import { useProjectPage } from '@/lib/use-project-page'

export function ProjectPageFrame({
  title,
  children,
}: {
  title: string
  children: (context: ReturnType<typeof useProjectPage>) => ReactNode
}) {
  const context = useProjectPage()

  if (context.isPending) return <FullPageSpinner />

  if (!context.project) {
    return (
      <div className="mx-auto flex w-full max-w-5xl flex-col gap-2">
        <h1 className="text-xl font-semibold tracking-tight">Project not found</h1>
        <p className="text-muted-foreground text-sm">
          This project doesn&rsquo;t exist or you don&rsquo;t have access to it.
        </p>
      </div>
    )
  }

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'organization', label: context.activeOrg.name, to: '/' },
          { id: 'project', label: context.project.name },
          { id: 'page', label: title },
        ]}
      />
      {children(context)}
    </div>
  )
}
