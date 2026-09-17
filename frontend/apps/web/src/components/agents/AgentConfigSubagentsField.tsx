import { useAgentProfiles } from '@omnara/react'

import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  type BasicSubagent,
  newSubagent,
  subagentKeyError,
  type SubagentType,
} from '@/components/agents/agentConfigSubagents'
import { PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Field,
  FieldDescription,
  FieldError,
  FieldLabel,
  RequiredFieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
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

function subagentTypeLabel(type: SubagentType) {
  return subagentTypeOptions.find((option) => option.value === type)?.label ?? type
}

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
        <div className="divide-y">
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
          <div className="grid gap-4 px-5 py-4 sm:grid-cols-2">
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
  nameError,
  onChange,
  onRemove,
}: {
  orgId: string
  projectId: string
  subagent: BasicSubagent
  nameError: string | undefined
  onChange: (fields: Partial<BasicSubagent>) => void
  onRemove: () => void
}) {
  const fieldId = (name: string) => `agent-config-subagent-${subagent.id}-${name}`
  return (
    <div className="space-y-4 px-5 py-4">
      <Field data-invalid={nameError !== undefined}>
        <RequiredFieldLabel htmlFor={fieldId('name')}>Subagent name</RequiredFieldLabel>
        <div className="flex items-start gap-2">
          <Input
            id={fieldId('name')}
            className="min-w-0 flex-1"
            value={subagent.key}
            placeholder="researcher"
            aria-invalid={nameError !== undefined}
            onChange={(event) => {
              onChange({ key: event.target.value.trim() })
            }}
          />
          <Button
            type="button"
            size="icon"
            variant="ghost"
            className="shrink-0"
            aria-label={`Remove subagent ${subagent.key || 'entry'}`}
            onClick={onRemove}
          >
            <Trash2Icon />
          </Button>
        </div>
        <FieldError>{nameError}</FieldError>
      </Field>
      <div className="grid gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={fieldId('type')}>Subagent type</FieldLabel>
          <Select
            value={subagent.type}
            onValueChange={(value) => {
              const option = subagentTypeOptions.find((candidate) => candidate.value === value)
              if (option) onChange({ type: option.value })
            }}
          >
            <SelectTrigger id={fieldId('type')} className="w-full">
              <SelectValue>{subagentTypeLabel(subagent.type)}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {subagentTypeOptions.map((option) => (
                <SelectItem key={option.value} value={option.value}>
                  {option.label}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        {subagent.type === 'profile' && (
          <Field>
            <RequiredFieldLabel htmlFor={fieldId('profile')}>Agent profile</RequiredFieldLabel>
            <ProfileNameField
              id={fieldId('profile')}
              orgId={orgId}
              projectId={projectId}
              value={subagent.profileName}
              onChange={(profileName) => {
                onChange({ profileName })
              }}
            />
          </Field>
        )}
      </div>
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
        <FieldDescription>
          The model reads this description to decide when to use this subagent.
        </FieldDescription>
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
          <FieldLabel htmlFor={fieldId('archive-idle')}>Archive after idle (minutes)</FieldLabel>
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
