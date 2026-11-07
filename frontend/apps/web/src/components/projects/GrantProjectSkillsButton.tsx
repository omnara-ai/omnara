import { useGrantSkillToProject, useProjectAvailableSkills, useSkills } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { useState } from 'react'

import { GrantProjectResourcesDialog } from '@/components/projects/GrantProjectResourcesDialog'
import { Button } from '@/components/ui/button'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const SkillMultiCombobox = createResourceMultiCombobox<Skill>({
  itemKey: (skill) => skill.id,
  itemLabel: (skill) => skill.name,
  renderItem: (skill) => (
    <span className="flex min-w-0 flex-col">
      <span className="truncate">{skill.name}</span>
      <span className="text-muted-foreground text-xs">
        {skill.owner.kind === 'org' ? 'Organization' : 'Personal'}
      </span>
    </span>
  ),
  placeholder: 'Search skills…',
  emptyMessage: 'No skills to share.',
})

/**
 * "Share skills" for a project's Shared tab: grants the viewer's personal skills, and
 * organization skills for org admins, to the project. The caller checks the viewer can
 * manage the project.
 */
export function GrantProjectSkillsButton({
  orgId,
  projectId,
}: {
  orgId: string
  projectId: string
}) {
  const [open, setOpen] = useState(false)
  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => {
          setOpen(true)
        }}
      >
        Share skills
      </Button>
      {open && (
        <GrantProjectSkillsDialog
          open={open}
          onOpenChange={setOpen}
          orgId={orgId}
          projectId={projectId}
        />
      )}
    </>
  )
}

function GrantProjectSkillsDialog({
  open,
  onOpenChange,
  orgId,
  projectId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  projectId: string
}) {
  const { activeOrg } = useActiveOrg()
  const canShareOrgSkills = canManageOrg(activeOrg.role)
  const grantSkill = useGrantSkillToProject(orgId)
  const search = useTypeaheadSearch()
  const skillsQuery = useSkills(orgId, undefined, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const sharedQuery = useProjectAvailableSkills(orgId, projectId, {
    filters: { availability_source: 'grant' },
    pageSize: 100,
  })
  const shared = useCompleteInfiniteQueryItems(sharedQuery, open)
  const sharedIds = new Set(shared.items.map((access) => access.skill.id))
  // Other projects' skills can't be shared into this one.
  const skills = useInfiniteQueryItems(skillsQuery).filter(
    (skill) =>
      (skill.owner.kind === 'user' || (skill.owner.kind === 'org' && canShareOrgSkills)) &&
      !sharedIds.has(skill.id),
  )

  return (
    <GrantProjectResourcesDialog
      open={open}
      onOpenChange={onOpenChange}
      noun="skill"
      description="Let agents in this project use one of your skills."
      combobox={SkillMultiCombobox}
      itemKey={(skill) => skill.id}
      items={skills}
      search={search}
      query={skillsQuery}
      pending={skillsQuery.isPending || !shared.isComplete}
      isError={skillsQuery.isError || sharedQuery.isError}
      onRetry={() => {
        void Promise.all([skillsQuery.refetch(), sharedQuery.refetch()])
      }}
      emptyHint={
        canShareOrgSkills
          ? 'Every personal and organization skill is already shared, or none exist yet.'
          : 'Every personal skill is already shared, or you have none yet.'
      }
      grant={async (skill) => {
        await grantSkill.mutateAsync({ skillID: skill.id, projectID: projectId })
      }}
    />
  )
}
