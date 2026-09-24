import { useAgentProfiles } from '@omnara/react'
import { useState } from 'react'

import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  type BasicSubagent,
  newSubagent,
  subagentKeyError,
  type SubagentType,
} from '@/components/agents/agentConfigSubagents'
import { PillTabs } from '@/components/agents/PillTabs'
import { ChevronRightIcon, PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { CollapseBody } from '@/components/ui/collapse-body'
import { Collapsible, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { Textarea } from '@/components/ui/textarea'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

interface ProfileOption {
  name: string
}

const ProfileNameCombobox = createResourceCombobox<ProfileOption>({
  itemKey: (item) => item.name,
  itemLabel: (item) => item.name,
  placeholder: 'Search agent profiles…',
  emptyMessage: 'No agent profiles found.',
})

const subagentTypeOptions: { value: SubagentType; label: string }[] = [
  { value: 'profile', label: 'Agent profile' },
  { value: 'self', label: 'Clone' },
]

export function AgentConfigSubagentsField({
  orgId,
  projectId,
  subagents,
  maxSubagents,
  maxDepth,
  onSubagentsChange,
  onMaxSubagentsChange,
  onMaxDepthChange,
}: {
  orgId: string
  projectId: string
  subagents: BasicSubagent[]
  maxSubagents: string
  maxDepth: string
  onSubagentsChange: (subagents: BasicSubagent[]) => void
  onMaxSubagentsChange: (value: string) => void
  onMaxDepthChange: (value: string) => void
}) {
  const [expandedIds, setExpandedIds] = useState<ReadonlySet<string>>(new Set())
  function setExpanded(id: string, expanded: boolean) {
    setExpandedIds((prev) => {
      const next = new Set(prev)
      if (expanded) {
        next.add(id)
      } else {
        next.delete(id)
      }
      return next
    })
  }

  const update = (id: string, fields: Partial<BasicSubagent>) => {
    onSubagentsChange(
      subagents.map((subagent) => (subagent.id === id ? { ...subagent, ...fields } : subagent)),
    )
  }

  return (
    <AgentConfigSectionCard
      title="Subagents"
      action={
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-10 sm:size-8"
          aria-label="Add subagent"
          onClick={() => {
            onSubagentsChange([...subagents, newSubagent()])
          }}
        >
          <PlusIcon />
        </Button>
      }
    >
      {subagents.length > 0 ? (
        <div className="flex flex-col gap-1 px-3 pb-3">
          {subagents.map((subagent) => {
            const duplicateName = subagents.some(
              (candidate) => candidate.id !== subagent.id && candidate.key === subagent.key,
            )
            return (
              <SubagentFields
                key={subagent.id}
                orgId={orgId}
                projectId={projectId}
                subagent={subagent}
                expanded={expandedIds.has(subagent.id)}
                onExpandedChange={(expanded) => {
                  setExpanded(subagent.id, expanded)
                }}
                nameError={
                  subagent.key === ''
                    ? undefined
                    : (subagentKeyError(subagent.key) ??
                      (duplicateName
                        ? 'Name must be unique within this configuration.'
                        : undefined))
                }
                onChange={(fields) => {
                  update(subagent.id, fields)
                }}
                onRemove={() => {
                  onSubagentsChange(subagents.filter((entry) => entry.id !== subagent.id))
                }}
              />
            )
          })}
          <div className="grid gap-4 px-2 pt-3 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor="agent-config-max-subagents">Max active subagents</FieldLabel>
              <Input
                id="agent-config-max-subagents"
                inputMode="numeric"
                value={maxSubagents}
                placeholder="Unlimited"
                onChange={(event) => {
                  onMaxSubagentsChange(event.target.value.trim())
                }}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="agent-config-max-depth">Max depth</FieldLabel>
              <Input
                id="agent-config-max-depth"
                inputMode="numeric"
                value={maxDepth}
                placeholder="1"
                onChange={(event) => {
                  onMaxDepthChange(event.target.value.trim())
                }}
              />
            </Field>
          </div>
        </div>
      ) : null}
    </AgentConfigSectionCard>
  )
}

function SubagentFields({
  orgId,
  projectId,
  subagent,
  expanded,
  onExpandedChange,
  nameError,
  onChange,
  onRemove,
}: {
  orgId: string
  projectId: string
  subagent: BasicSubagent
  expanded: boolean
  onExpandedChange: (expanded: boolean) => void
  nameError: string | undefined
  onChange: (fields: Partial<BasicSubagent>) => void
  onRemove: () => void
}) {
  const fieldId = (name: string) => `agent-config-subagent-${subagent.id}-${name}`
  function edit(fields: Partial<BasicSubagent>) {
    onExpandedChange(true)
    onChange(fields)
  }
  return (
    <Collapsible
      open={expanded}
      onOpenChange={onExpandedChange}
      className="expanded-surface rounded-xl"
    >
      <div className="flex flex-wrap items-center gap-2 py-2 pl-1 pr-2 sm:flex-nowrap">
        <CollapsibleTrigger asChild>
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="text-muted-foreground group size-10 sm:size-8"
            aria-label="Toggle subagent details"
          >
            <ChevronRightIcon className="size-4 transition-transform group-data-[state=open]:rotate-90" />
          </Button>
        </CollapsibleTrigger>
        <PillTabs
          value={subagent.type}
          onValueChange={(type) => {
            edit({ type })
          }}
          tabs={subagentTypeOptions}
        />
        <Input
          id={fieldId('name')}
          className="min-w-0 flex-1"
          value={subagent.key}
          placeholder="Subagent name"
          aria-label="Subagent name"
          aria-invalid={nameError !== undefined}
          onChange={(event) => {
            edit({ key: event.target.value.trim() })
          }}
        />
        {subagent.type === 'profile' && (
          <div className="min-w-0 flex-1 basis-full sm:basis-auto">
            <ProfileNameField
              id={fieldId('profile')}
              orgId={orgId}
              projectId={projectId}
              value={subagent.profileName}
              onChange={(profileName) => {
                edit({ profileName })
              }}
            />
          </div>
        )}
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="text-muted-foreground size-10 sm:size-8"
          aria-label={`Remove subagent ${subagent.key || 'entry'}`}
          onClick={onRemove}
        >
          <Trash2Icon />
        </Button>
      </div>
      <CollapseBody open={expanded}>
        <div className="flex flex-col gap-4 px-3 pb-5 pt-5 sm:pl-11">
          {nameError && <FieldError>{nameError}</FieldError>}
          <Field>
            <FieldLabel htmlFor={fieldId('description')}>Description</FieldLabel>
            <Input
              id={fieldId('description')}
              value={subagent.description}
              placeholder="Researches a topic and reports back a summary."
              onChange={(event) => {
                onChange({ description: event.target.value })
              }}
            />
          </Field>
          <Field>
            <FieldLabel htmlFor={fieldId('append')}>Extra instructions</FieldLabel>
            <Textarea
              id={fieldId('append')}
              value={subagent.instructionAppend}
              placeholder="Appended to the subagent's instruction."
              className="max-h-48 min-h-16 resize-y"
              onChange={(event) => {
                onChange({ instructionAppend: event.target.value })
              }}
            />
          </Field>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field>
              <FieldLabel htmlFor={fieldId('max-instances')}>Max instances</FieldLabel>
              <Input
                id={fieldId('max-instances')}
                inputMode="numeric"
                value={subagent.maxInstances}
                placeholder="Unlimited"
                onChange={(event) => {
                  onChange({ maxInstances: event.target.value.trim() })
                }}
              />
            </Field>
            <Field>
              <FieldLabel htmlFor={fieldId('archive-idle')}>
                Archive after idle (minutes)
              </FieldLabel>
              <Input
                id={fieldId('archive-idle')}
                inputMode="numeric"
                value={subagent.archiveAfterIdleMinutes}
                placeholder="Never"
                onChange={(event) => {
                  onChange({ archiveAfterIdleMinutes: event.target.value.trim() })
                }}
              />
            </Field>
          </div>
          {subagent.modelOverride !== undefined && (
            <p className="text-muted-foreground text-xs">
              This subagent overrides the model in YAML; edit that in the YAML view.
            </p>
          )}
        </div>
      </CollapseBody>
    </Collapsible>
  )
}

function ProfileNameField({
  id,
  orgId,
  projectId,
  value,
  onChange,
}: {
  id: string
  orgId: string
  projectId: string
  value: string
  onChange: (name: string) => void
}) {
  const search = useTypeaheadSearch()
  const query = useAgentProfiles(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const items = useInfiniteQueryItems(query).map(
    (profile): ProfileOption => ({ name: profile.name }),
  )
  return (
    <ProfileNameCombobox
      id={id}
      items={items}
      value={value === '' ? null : { name: value }}
      onValueChange={(item) => {
        onChange(item?.name ?? '')
      }}
      search={search}
      query={query}
      placeholder={query.isPending ? 'Loading profiles…' : 'Search agent profiles…'}
    />
  )
}
