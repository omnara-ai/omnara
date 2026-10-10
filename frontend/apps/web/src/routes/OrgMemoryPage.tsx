import { useOrgMemoryStores } from '@omnara/react'
import type { VisibleProject } from '@omnara/sdk'
import { useState } from 'react'

import { ChevronDown, Folder } from '@/components/icons'
import { MemoryStoreDialog } from '@/components/memory/MemoryStoreDialog'
import { MemoryStoresSection } from '@/components/memory/MemoryStoresSection'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { useProjectDirectory } from '@/hooks/use-project-directory'
import { useResourceList } from '@/hooks/use-resource-list'
import { useActiveOrg } from '@/lib/use-active-org'

export function OrgMemoryPage() {
  const { activeOrg } = useActiveOrg()
  const list = useResourceList('name')
  const query = useOrgMemoryStores(activeOrg.id, { filters: list.apiFilters })
  const { loaded } = useProjectDirectory(activeOrg.id)
  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <MemoryStoresSection
        list={list}
        query={query}
        action={<CreateStoreButton orgId={activeOrg.id} />}
        projectOf={(store) => loaded.get(store.project_id)}
      />
    </div>
  )
}

function CreateStoreButton({ orgId }: { orgId: string }) {
  const directory = useProjectDirectory(orgId)
  const [creatingIn, setCreatingIn] = useState<VisibleProject>()
  const projects = [...directory.projects.values()].filter((project) => project.access.can_manage)
  const [first, second] = projects
  if (!directory.isLoaded || !first) return null
  return (
    <>
      {second ? (
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button size="sm">
              Create store
              <ChevronDown aria-hidden="true" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent className="max-h-72 min-w-56" align="center">
            <DropdownMenuLabel className="text-muted-foreground text-xs">
              Choose a project
            </DropdownMenuLabel>
            {projects.map((project) => (
              <DropdownMenuItem
                key={project.id}
                className="gap-2"
                onSelect={() => {
                  setCreatingIn(project)
                }}
              >
                <Folder />
                <span className="truncate">{project.name}</span>
              </DropdownMenuItem>
            ))}
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <Button
          size="sm"
          onClick={() => {
            setCreatingIn(first)
          }}
        >
          Create store
        </Button>
      )}
      {creatingIn && (
        <MemoryStoreDialog
          orgId={orgId}
          projectId={creatingIn.id}
          projectName={creatingIn.name}
          onClose={() => {
            setCreatingIn(undefined)
          }}
        />
      )}
    </>
  )
}
