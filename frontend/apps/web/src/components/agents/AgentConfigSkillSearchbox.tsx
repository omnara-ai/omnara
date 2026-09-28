import { useProjectAvailableSkills } from '@omnara/react'
import type { Skill } from '@omnara/sdk'
import { useEffect, useRef, useState } from 'react'

import { skillComboboxConfig } from '@/components/agents/skillComboboxConfig'
import { PlusIcon } from '@/components/icons'
import { Combobox, ComboboxInput } from '@/components/ui/combobox'
import { ResourceComboboxContent } from '@/components/ui/resource-combobox-content'
import type { ResourceComboboxConfig } from '@/components/ui/resource-combobox-core'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

type SkillOption = { kind: 'create' } | { kind: 'skill'; skill: Skill }

const CREATE_OPTION: SkillOption = { kind: 'create' }

function optionKey(option: SkillOption) {
  return option.kind === 'create' ? 'create-skill' : option.skill.id
}

function matchesSearch(option: SkillOption, text: string) {
  return (
    option.kind === 'create' || option.skill.name.toLowerCase().includes(text.trim().toLowerCase())
  )
}

function orderOptions(skills: SkillOption[], text: string, canCreate: boolean) {
  if (!canCreate) return skills
  const searching = text.trim() !== ''
  return searching && skills.some((option) => matchesSearch(option, text))
    ? [...skills, CREATE_OPTION]
    : [CREATE_OPTION, ...skills]
}

function optionLabel(option: SkillOption) {
  return option.kind === 'create' ? 'Create skill' : option.skill.name
}

const skillOptionConfig: ResourceComboboxConfig<SkillOption> = {
  itemKey: optionKey,
  itemLabel: optionLabel,
  renderItem: (option) =>
    option.kind === 'create' ? (
      <span className="flex items-center gap-2 font-medium">
        <PlusIcon className="size-4" />
        Create skill
      </span>
    ) : (
      skillComboboxConfig.renderItem?.(option.skill)
    ),
  placeholder: skillComboboxConfig.placeholder,
}

export function AgentConfigSkillSearchbox({
  orgId,
  projectId,
  active,
  expanded,
  excludedIds,
  excludedNames,
  onSelect,
  onCreateSkill,
}: {
  orgId: string
  projectId: string
  active: boolean
  expanded: boolean
  excludedIds: ReadonlySet<string>
  excludedNames: ReadonlySet<string>
  onSelect: (skill: Skill) => void
  onCreateSkill?: () => void
}) {
  const inputRef = useRef<HTMLInputElement | null>(null)
  const initiallyActive = useRef(active)
  const [popupOpen, setPopupOpen] = useState(true)
  const search = useTypeaheadSearch()
  const query = useProjectAvailableSkills(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
    enabled: active,
  })
  const skills = useInfiniteQueryItems(query).flatMap(({ skill }): SkillOption[] =>
    excludedIds.has(skill.id) || excludedNames.has(skill.name) ? [] : [{ kind: 'skill', skill }],
  )
  const options = orderOptions(skills, search.search, onCreateSkill !== undefined)

  useEffect(() => {
    if (initiallyActive.current) inputRef.current?.focus()
  }, [])

  return (
    <Combobox
      items={options}
      inputValue={search.search}
      onInputValueChange={search.setSearch}
      itemToStringLabel={optionLabel}
      itemToStringValue={optionKey}
      isItemEqualToValue={(option, other) => optionKey(option) === optionKey(other)}
      filter={matchesSearch}
      autoHighlight
      open={active && expanded && popupOpen}
      onOpenChange={setPopupOpen}
      value={null}
      onValueChange={(option) => {
        if (!option) return
        if (option.kind === 'skill') onSelect(option.skill)
        else onCreateSkill?.()
      }}
    >
      <ComboboxInput
        ref={inputRef}
        showTrigger={false}
        className="h-9"
        aria-label={skillOptionConfig.placeholder}
        placeholder={active && query.isPending ? 'Loading skills…' : skillOptionConfig.placeholder}
      />
      <ResourceComboboxContent
        config={skillOptionConfig}
        pending={query.isPending}
        emptyMessage={skillComboboxConfig.emptyMessage ?? ''}
        query={query}
      />
    </Combobox>
  )
}
