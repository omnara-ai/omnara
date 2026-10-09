import { useDeleteSkill, useDeleteSkillGrant, useGrantSkillToProject } from '@omnara/react'
import { ApiError, type ProjectSkillAccess, type Skill } from '@omnara/sdk'
import { useState } from 'react'

import { Ellipsis } from '@/components/icons'
import { GrantToProjectDialog } from '@/components/projects/GrantToProjectDialog'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export function SkillRowActions({
  orgId,
  skill,
  availability,
  projectName,
  canDelete,
  canGrant = false,
  onRemoved,
}: {
  orgId: string
  skill: Skill
  availability?: ProjectSkillAccess['availability']
  projectName?: string
  canDelete: boolean
  canGrant?: boolean
  /** Called after the skill is deleted or unshared, e.g. to leave its page. */
  onRemoved?: () => void
}) {
  const deleteSkill = useDeleteSkill(orgId)
  const deleteGrant = useDeleteSkillGrant(orgId)
  const grantSkill = useGrantSkillToProject(orgId)
  const [grantOpen, setGrantOpen] = useState(false)
  const isGrant = availability?.source === 'grant'

  if (!canDelete && !canGrant) return null

  async function remove() {
    if (
      !window.confirm(isGrant ? 'Stop sharing this skill with the project?' : 'Delete this skill?')
    )
      return
    try {
      if (availability?.source === 'grant') {
        await deleteGrant.mutateAsync({ skillID: skill.id, grantID: availability.grant_id })
      } else {
        await deleteSkill.mutateAsync(skill.id)
      }
      onRemoved?.()
    } catch (error) {
      window.alert(error instanceof ApiError ? error.message : 'Could not remove skill')
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger asChild>
          <Button variant="ghost" size="icon" aria-label="Skill actions">
            <Ellipsis />
          </Button>
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          {canGrant && (
            <DropdownMenuItem
              onSelect={() => {
                setGrantOpen(true)
              }}
            >
              Share with project
            </DropdownMenuItem>
          )}
          {canDelete && (
            <DropdownMenuItem
              variant="destructive"
              className={isGrant ? 'items-start' : undefined}
              onSelect={() => {
                void remove()
              }}
            >
              <RemoveSkillLabel isGrant={isGrant} projectName={projectName} />
            </DropdownMenuItem>
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      {canGrant && grantOpen && (
        <GrantToProjectDialog
          open
          onOpenChange={(nextOpen) => {
            if (!nextOpen) setGrantOpen(false)
          }}
          orgId={orgId}
          resourceName={skill.name}
          isProjectEligible={(project) => project.access.can_manage}
          excludedProjectIds={skill.owner.kind === 'project' ? [skill.owner.project_id] : []}
          onGrant={(projectID) => grantSkill.mutateAsync({ projectID, skillID: skill.id })}
        />
      )}
    </>
  )
}

function RemoveSkillLabel({ isGrant, projectName }: { isGrant: boolean; projectName?: string }) {
  if (!isGrant) return 'Delete'
  return (
    <span className="flex flex-col gap-0.5">
      <span>Stop sharing skill</span>
      <span className="text-muted-foreground text-xs font-normal">
        Removes {projectName ?? 'this project'}&rsquo;s access to this skill
      </span>
    </span>
  )
}
