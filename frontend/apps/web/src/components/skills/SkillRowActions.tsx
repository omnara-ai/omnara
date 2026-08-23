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
}: {
  orgId: string
  skill: Skill
  availability?: ProjectSkillAccess['availability']
  projectName?: string
  canDelete: boolean
  canGrant?: boolean
}) {
  const deleteSkill = useDeleteSkill(orgId)
  const deleteGrant = useDeleteSkillGrant(orgId)
  const grantSkill = useGrantSkillToProject(orgId)
  const [grantOpen, setGrantOpen] = useState(false)
  const isGrant = availability?.source === 'grant'

  if (!canDelete && !canGrant) return null

  async function remove() {
    if (!window.confirm(isGrant ? 'Remove this skill grant?' : 'Delete this skill?')) return
    try {
      if (availability?.source === 'grant') {
        await deleteGrant.mutateAsync({ skillID: skill.id, grantID: availability.grant_id })
      } else {
        await deleteSkill.mutateAsync(skill.id)
      }
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
              Grant to project
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
              {isGrant ? (
                <span className="flex flex-col gap-0.5">
                  <span>Remove skill grant</span>
                  <span className="text-muted-foreground text-xs font-normal">
                    Removes {projectName ?? 'this project'}&rsquo;s access to this skill
                  </span>
                </span>
              ) : (
                'Delete'
              )}
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
