import { useProjectAvailableSkills } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { useEffect, useState } from 'react'

import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { CircleAlert, PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { exactNameGlob, useTypeaheadSearch } from '@/hooks/use-resource-list'
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
  onUnavailableIdsChange,
}: {
  orgId: string
  projectId: string
  selectedIds: string[]
  onSelectedIdsChange: (ids: string[]) => void
  onUnavailableIdsChange: (ids: string[]) => void
}) {
  const [draftOpen, setDraftOpen] = useState(false)

  const [resolvedSkills, setResolvedSkills] = useState<ReadonlyMap<string, Skill>>(new Map())
  const skillById = (id: string) => resolvedSkills.get(id)

  const unresolvedIds = selectedIds.filter((id) => !resolvedSkills.has(id))
  const resolveQuery = useProjectAvailableSkills(orgId, projectId, {
    sort: 'name',
    pageSize: 100,
    enabled: unresolvedIds.length > 0,
  })
  const completeResolve = useCompleteInfiniteQueryItems(resolveQuery, unresolvedIds.length > 0)
  const selectedSet = new Set(selectedIds)
  const resolvableNow = completeResolve.items.flatMap(({ skill }) =>
    selectedSet.has(skill.id) && !resolvedSkills.has(skill.id) ? [skill] : [],
  )
  if (resolvableNow.length > 0) {
    const next = new Map(resolvedSkills)
    for (const skill of resolvableNow) next.set(skill.id, skill)
    setResolvedSkills(next)
  }
  const resolveInventoryIds = new Set(completeResolve.items.map((access) => access.skill.id))
  const danglingIds = completeResolve.isComplete
    ? unresolvedIds.filter((id) => !resolveInventoryIds.has(id))
    : []
  const danglingIdSet = new Set(danglingIds)

  const selectedNames = new Set(
    selectedIds.flatMap((id) => {
      const skill = skillById(id)
      return skill ? [skill.name] : []
    }),
  )

  const [unavailableIds, setUnavailableIds] = useState<ReadonlySet<string>>(new Set())
  const reportAvailability = (id: string, availableNow: boolean) => {
    setUnavailableIds((prev) => {
      if (prev.has(id) !== availableNow) return prev
      const next = new Set(prev)
      if (availableNow) {
        next.delete(id)
      } else {
        next.add(id)
      }
      return next
    })
  }
  const unavailableReportKey = [...new Set([...unavailableIds, ...danglingIds])].join('\n')
  useEffect(() => {
    onUnavailableIdsChange(unavailableReportKey === '' ? [] : unavailableReportKey.split('\n'))
  }, [onUnavailableIdsChange, unavailableReportKey])

  const selectSkill = (skill: Skill, replacedId?: string) => {
    setResolvedSkills((prev) => new Map(prev).set(skill.id, skill))
    if (replacedId === undefined) {
      onSelectedIdsChange([...selectedIds, skill.id])
      setDraftOpen(false)
    } else {
      onSelectedIdsChange(selectedIds.map((id) => (id === replacedId ? skill.id : id)))
    }
  }

  return (
    <AgentConfigSectionCard
      title="Skills"
      action={
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-8"
          aria-label="Add skill"
          onClick={() => {
            setDraftOpen(true)
          }}
        >
          <PlusIcon />
        </Button>
      }
    >
      {draftOpen || selectedIds.length > 0 ? (
        <div className="space-y-2 px-5 py-4">
          {selectedIds.map((id) => (
            <SelectedSkillRow
              key={id}
              orgId={orgId}
              projectId={projectId}
              id={id}
              skill={skillById(id)}
              dangling={danglingIdSet.has(id)}
              excludedIds={selectedSet}
              excludedNames={selectedNames}
              onAvailabilityChange={reportAvailability}
              onReplace={(skill) => {
                selectSkill(skill, id)
              }}
              onRemove={() => {
                onSelectedIdsChange(selectedIds.filter((selectedId) => selectedId !== id))
              }}
            />
          ))}
          {draftOpen && (
            <SkillEntryRow
              orgId={orgId}
              projectId={projectId}
              skill={null}
              excludedIds={selectedSet}
              excludedNames={selectedNames}
              removeLabel="Remove skill entry"
              onSelect={(skill) => {
                selectSkill(skill)
              }}
              onRemove={() => {
                setDraftOpen(false)
              }}
            />
          )}
        </div>
      ) : null}
    </AgentConfigSectionCard>
  )
}

function SkillEntryRow({
  orgId,
  projectId,
  skill,
  excludedIds,
  excludedNames,
  removeLabel,
  onSelect,
  onRemove,
}: {
  orgId: string
  projectId: string
  skill: Skill | null
  excludedIds: ReadonlySet<string>
  excludedNames: ReadonlySet<string>
  removeLabel: string
  onSelect: (skill: Skill) => void
  onRemove: () => void
}) {
  const search = useTypeaheadSearch()
  const query = useProjectAvailableSkills(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const loadedSkills = useInfiniteQueryItems(query).map((access) => access.skill)
  const items = loadedSkills.filter(
    (candidate) =>
      candidate.id === skill?.id ||
      (!excludedIds.has(candidate.id) && !excludedNames.has(candidate.name)),
  )
  return (
    <div className="flex items-center gap-2">
      <div className="min-w-0 flex-1">
        <SkillCombobox
          items={items}
          value={skill}
          onValueChange={(next) => {
            if (next && next.id !== skill?.id) onSelect(next)
          }}
          search={search}
          query={query}
          placeholder={query.isPending ? 'Loading skills…' : 'Search skills…'}
        />
      </div>
      <Button type="button" size="icon" variant="ghost" aria-label={removeLabel} onClick={onRemove}>
        <Trash2Icon />
      </Button>
    </div>
  )
}

function SelectedSkillRow({
  orgId,
  projectId,
  id,
  skill,
  dangling,
  excludedIds,
  excludedNames,
  onAvailabilityChange,
  onReplace,
  onRemove,
}: {
  orgId: string
  projectId: string
  id: string
  skill: Skill | undefined
  dangling: boolean
  excludedIds: ReadonlySet<string>
  excludedNames: ReadonlySet<string>
  onAvailabilityChange: (id: string, available: boolean) => void
  onReplace: (skill: Skill) => void
  onRemove: () => void
}) {
  const lookupQuery = useProjectAvailableSkills(orgId, projectId, {
    filters: { name: exactNameGlob(skill?.name ?? '') },
    pageSize: 25,
    enabled: skill !== undefined,
  })
  const lookupItems = useInfiniteQueryItems(lookupQuery)
  const unavailable =
    dangling ||
    (skill !== undefined &&
      lookupQuery.isSuccess &&
      !lookupItems.some((access) => access.skill.id === id))
  useEffect(() => {
    onAvailabilityChange(id, !unavailable)
    return () => {
      onAvailabilityChange(id, true)
    }
  }, [id, onAvailabilityChange, unavailable])

  if (skill && !unavailable) {
    return (
      <SkillEntryRow
        orgId={orgId}
        projectId={projectId}
        skill={skill}
        excludedIds={excludedIds}
        excludedNames={excludedNames}
        removeLabel={`Detach ${skill.name}`}
        onSelect={onReplace}
        onRemove={onRemove}
      />
    )
  }
  return (
    <div
      className={
        unavailable
          ? 'border-destructive/40 bg-destructive/5 flex items-center gap-3 rounded-md border px-3 py-2.5'
          : 'border-border bg-muted/40 flex items-center gap-3 rounded-md border px-3 py-2.5'
      }
    >
      {unavailable && (
        <div className="bg-muted flex size-8 shrink-0 items-center justify-center rounded-md">
          <CircleAlert className="text-destructive size-4" />
        </div>
      )}
      <div className="min-w-0 flex-1">
        {skill ? (
          <>
            <div className="flex items-center gap-2">
              <span className="truncate text-sm font-medium">{skill.name}</span>
              <span className="text-muted-foreground shrink-0 text-xs">
                {skillOwnerLabel(skill)} · v{skill.revision}
              </span>
            </div>
            <p className="text-muted-foreground truncate text-xs">
              Skill is no longer available to this project.
            </p>
          </>
        ) : (
          <>
            <p className={unavailable ? 'text-destructive text-sm font-medium' : 'text-sm'}>
              {unavailable ? 'Skill is no longer available' : 'Loading skill…'}
            </p>
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
