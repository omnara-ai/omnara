import { useMemoryStores } from '@omnara/react'
import { useState } from 'react'

import { MemoryStoreDialog } from '@/components/memory/MemoryStoreDialog'
import { MemoryStoresSection } from '@/components/memory/MemoryStoresSection'
import { ProjectPageFrame } from '@/components/projects/ProjectPageFrame'
import { Button } from '@/components/ui/button'
import { useResourceList } from '@/hooks/use-resource-list'

export function ProjectMemoryPage() {
  return (
    <ProjectPageFrame>
      {({ activeOrg, projectId, project }) =>
        project?.access.can_read ? (
          <MemoryStores
            key={projectId}
            orgId={activeOrg.id}
            projectId={projectId}
            canManage={project.access.can_manage}
          />
        ) : (
          <p className="text-muted-foreground text-sm">
            You don’t have permission to view memory stores here.
          </p>
        )
      }
    </ProjectPageFrame>
  )
}

function MemoryStores({
  orgId,
  projectId,
  canManage,
}: {
  orgId: string
  projectId: string
  canManage: boolean
}) {
  const list = useResourceList('name')
  const query = useMemoryStores(orgId, projectId, { filters: list.apiFilters })
  const [creating, setCreating] = useState(false)
  return (
    <>
      <MemoryStoresSection
        list={list}
        query={query}
        action={
          canManage && (
            <Button
              size="sm"
              onClick={() => {
                setCreating(true)
              }}
            >
              Create store
            </Button>
          )
        }
      />
      {creating && (
        <MemoryStoreDialog
          orgId={orgId}
          projectId={projectId}
          onClose={() => {
            setCreating(false)
          }}
        />
      )}
    </>
  )
}
