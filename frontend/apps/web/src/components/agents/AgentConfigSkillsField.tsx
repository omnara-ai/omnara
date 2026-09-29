import { exactNameGlob, useProjectAvailableSkills } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import { AgentConfigSkillSearchbox } from '@/components/agents/AgentConfigSkillSearchbox'
import { CircleAlert, PlusIcon, Trash2Icon } from '@/components/icons'
import { CreateSkillDialog } from '@/components/org/CreateSkillDialog'
import { Button } from '@/components/ui/button'
import { useCompleteInfiniteQueryItems } from '@/hooks/use-complete-infinite-query-items'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useProjectPage } from '@/lib/use-project-page'
import { cn } from '@/lib/utils'

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
  const { project } = useProjectPage()
  const canCreateSkills = project?.access.can_manage ?? false
  const [searchOpen, setSearchOpen] = useState(false)
  const [searchKey, setSearchKey] = useState(0)
  const [searchExpanded, setSearchExpanded] = useState(false)
  const searchWrapperRef = useRef<HTMLDivElement | null>(null)

  useEffect(() => {
    const wrapper = searchWrapperRef.current
    if (!searchOpen || !wrapper) return
    let current = true
    void Promise.allSettled(wrapper.getAnimations().map((animation) => animation.finished)).then(
      () => {
        if (current) setSearchExpanded(true)
      },
    )
    return () => {
      current = false
    }
  }, [searchOpen, searchKey])

  function openSearch() {
    setSearchKey((key) => key + 1)
    setSearchOpen(true)
    setSearchExpanded(false)
  }

  function closeSearch() {
    setSearchOpen(false)
    setSearchExpanded(false)
  }
  const [createOpen, setCreateOpen] = useState(false)

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

  const selectedSkills = selectedIds.flatMap((id) => {
    const skill = skillById(id)
    return skill ? [skill] : []
  })
  const selectedNames = new Set(selectedSkills.map((skill) => skill.name))

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

  const selectSkills = (skills: Skill[]) => {
    setResolvedSkills((prev) => {
      const next = new Map(prev)
      for (const skill of skills) next.set(skill.id, skill)
      return next
    })
    const addedIdByName = new Map(skills.map((skill) => [skill.name, skill.id]))
    const keptIds = selectedIds.filter((id) => {
      const name = skillById(id)?.name
      return name === undefined || (addedIdByName.get(name) ?? id) === id
    })
    const keptIdSet = new Set(keptIds)
    const addedIds = [...addedIdByName.values()].filter((id) => !keptIdSet.has(id))
    if (addedIds.length > 0 || keptIds.length < selectedIds.length) {
      onSelectedIdsChange([...keptIds, ...addedIds])
    }
    closeSearch()
  }

  return (
    <AgentConfigSectionCard
      title="Skills"
      action={
        <div className="flex min-w-0 flex-1 items-center justify-end gap-1">
          <div
            ref={searchWrapperRef}
            inert={!searchOpen}
            className={cn(
              'min-w-0 transition-[max-width,opacity] duration-200 ease-out motion-reduce:transition-none',
              searchOpen ? 'max-w-sm flex-1 opacity-100' : 'max-w-0 overflow-hidden opacity-0',
            )}
          >
            <AgentConfigSkillSearchbox
              key={searchKey}
              orgId={orgId}
              projectId={projectId}
              active={searchOpen}
              expanded={searchExpanded}
              excludedIds={selectedSet}
              excludedNames={selectedNames}
              onSelect={(skill) => {
                selectSkills([skill])
              }}
              onCreateSkill={
                canCreateSkills
                  ? () => {
                      closeSearch()
                      setCreateOpen(true)
                    }
                  : undefined
              }
            />
          </div>
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="text-muted-foreground size-10 sm:size-8"
            aria-label={searchOpen ? 'Close skill search' : 'Add skill'}
            aria-expanded={searchOpen}
            onClick={() => {
              if (searchOpen) closeSearch()
              else openSearch()
            }}
          >
            <PlusIcon
              className={cn(
                'transition-transform duration-200 motion-reduce:transition-none',
                searchOpen && 'rotate-45',
              )}
            />
          </Button>
        </div>
      }
    >
      {selectedIds.length > 0 ? (
        <div className="flex flex-col gap-1 px-3 pb-3">
          {selectedIds.map((id) => (
            <SelectedSkillRow
              key={id}
              orgId={orgId}
              projectId={projectId}
              id={id}
              skill={skillById(id)}
              dangling={danglingIdSet.has(id)}
              onAvailabilityChange={reportAvailability}
              onRemove={() => {
                onSelectedIdsChange(selectedIds.filter((selectedId) => selectedId !== id))
              }}
            />
          ))}
        </div>
      ) : null}
      {createOpen && (
        <CreateSkillDialog
          open
          onOpenChange={setCreateOpen}
          orgId={orgId}
          owner={{ kind: 'project', project_id: projectId }}
          attachedSkills={selectedSkills}
          onCreated={(skills) => {
            selectSkills(skills)
          }}
        />
      )}
    </AgentConfigSectionCard>
  )
}

function SelectedSkillRow({
  orgId,
  projectId,
  id,
  skill,
  dangling,
  onAvailabilityChange,
  onRemove,
}: {
  orgId: string
  projectId: string
  id: string
  skill: Skill | undefined
  dangling: boolean
  onAvailabilityChange: (id: string, available: boolean) => void
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

  return (
    <div className="flex items-center gap-2 py-2 pl-2 pr-2">
      {unavailable && <CircleAlert className="text-destructive size-4 shrink-0" />}
      <div className="min-w-0 flex-1">
        {skill ? (
          <>
            <p className="truncate text-sm font-medium">{skill.name}</p>
            <p className="text-muted-foreground truncate text-xs">
              {unavailable ? 'Skill is no longer available to this project.' : skill.description}
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
        className="text-muted-foreground size-10 sm:size-8"
        aria-label={skill ? `Detach ${skill.name}` : `Detach skill ${id}`}
        onClick={onRemove}
      >
        <Trash2Icon />
      </Button>
    </div>
  )
}
