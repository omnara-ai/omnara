import { useProjectAvailableSkills } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { Sparkles, Trash2Icon } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { skillOwnerLabel } from '@/lib/skills'

const SkillCombobox = createResourceCombobox<Skill>({
  itemKey: (skill) => skill.id,
  itemLabel: (skill) => skill.name,
  renderItem: (skill) => (
    <span className="flex min-w-0 flex-col gap-0.5">
      <span className="flex items-center justify-between gap-3">
        <span className="font-medium">{skill.name}</span>
        <span className="text-muted-foreground shrink-0 text-xs">
          {skillOwnerLabel(skill)} · v{skill.revision}
        </span>
      </span>
      <span className="text-muted-foreground line-clamp-2 text-xs">{skill.description}</span>
    </span>
  ),
  placeholder: 'Search skills…',
  emptyMessage: 'No available skills found.',
})

export function AgentConfigSkillsField({
  orgId,
  projectId,
  selectedIds,
  onSelectedIdsChange,
}: {
  orgId: string
  projectId: string
  selectedIds: string[]
  onSelectedIdsChange: (ids: string[]) => void
}) {
  const search = useTypeaheadSearch()
  const skillsQuery = useProjectAvailableSkills(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const loadedSkills = useInfiniteQueryItems(skillsQuery).map((access) => access.skill)
  const [pickedSkills, setPickedSkills] = useState<ReadonlyMap<string, Skill>>(new Map())
  const loadedSkillsById = new Map(loadedSkills.map((skill) => [skill.id, skill]))
  const skillById = (id: string) => pickedSkills.get(id) ?? loadedSkillsById.get(id)

  const selectedIdSet = new Set(selectedIds)
  const selectedNames = new Set(
    selectedIds.flatMap((id) => {
      const skill = skillById(id)
      return skill ? [skill.name] : []
    }),
  )
  const available = loadedSkills.filter(
    (skill) => !selectedIdSet.has(skill.id) && !selectedNames.has(skill.name),
  )

  return (
    <Field>
      <div className="flex items-center justify-between gap-3">
        <div>
          <FieldLabel>Skills</FieldLabel>
          <FieldDescription>
            Reusable instructions and files this agent can load on demand.
          </FieldDescription>
        </div>
        <div className="w-72">
          <SkillCombobox
            items={available}
            value={null}
            onValueChange={(skill) => {
              if (!skill) return
              setPickedSkills((prev) => new Map(prev).set(skill.id, skill))
              onSelectedIdsChange([...selectedIds, skill.id])
              search.setSearch('')
            }}
            search={search}
            query={skillsQuery}
            placeholder={skillsQuery.isPending ? 'Loading skills…' : 'Search skills…'}
          />
        </div>
      </div>
      <div className="space-y-2">
        {selectedIds.length === 0 ? (
          <div className="border-border bg-background/60 text-muted-foreground flex min-h-20 items-center justify-center gap-2 rounded-md border border-dashed px-4 text-sm">
            <Sparkles className="size-4" />
            No skills attached
          </div>
        ) : (
          selectedIds.map((id) => (
            <SelectedSkillRow
              key={id}
              id={id}
              skill={skillById(id)}
              onRemove={() => {
                setPickedSkills((prev) => {
                  const next = new Map(prev)
                  next.delete(id)
                  return next
                })
                onSelectedIdsChange(selectedIds.filter((selectedId) => selectedId !== id))
              }}
            />
          ))
        )}
      </div>
    </Field>
  )
}

function SelectedSkillRow({
  id,
  skill,
  onRemove,
}: {
  id: string
  skill: Skill | undefined
  onRemove: () => void
}) {
  return (
    <div className="border-border bg-background flex items-center gap-3 rounded-md border px-3 py-2.5">
      <div className="bg-muted flex size-8 shrink-0 items-center justify-center rounded-md">
        <Sparkles className="text-muted-foreground size-4" />
      </div>
      <div className="min-w-0 flex-1">
        {skill ? (
          <>
            <div className="flex items-center gap-2">
              <span className="truncate text-sm font-medium">{skill.name}</span>
              <span className="text-muted-foreground shrink-0 text-xs">
                {skillOwnerLabel(skill)} · v{skill.revision}
              </span>
            </div>
            <p className="text-muted-foreground truncate text-xs">{skill.description}</p>
          </>
        ) : (
          <>
            <p className="text-sm">Loading skill…</p>
            <p className="text-muted-foreground truncate font-mono text-xs">{id}</p>
          </>
        )}
      </div>
      <Button
        type="button"
        size="icon"
        variant="ghost"
        aria-label={skill ? `Detach ${skill.name}` : `Detach skill ${id}`}
        onClick={onRemove}
      >
        <Trash2Icon />
      </Button>
    </div>
  )
}
