import { useAgentProfiles } from '@omnara/react'

import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import {
  type BasicSubagent,
  newSubagent,
  subagentHandleError,
  type SubagentType,
} from '@/components/agents/useAgentBuilderForm'
import { PlusIcon, Trash2Icon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
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

const subagentTypeLabels: Record<SubagentType, string> = {
  profile: 'Profile',
  self: 'Copy of this agent',
}

export function AgentConfigSubagentsField({
  orgId,
  projectId,
  subagents,
  maxSubagents,
  onSubagentsChange,
  onMaxSubagentsChange,
}: {
  orgId: string
  projectId: string
  subagents: BasicSubagent[]
  maxSubagents: string
  onSubagentsChange: (subagents: BasicSubagent[]) => void
  onMaxSubagentsChange: (value: string) => void
}) {
  const handleCounts = new Map<string, number>()
  for (const subagent of subagents) {
    handleCounts.set(subagent.handle, (handleCounts.get(subagent.handle) ?? 0) + 1)
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
        <div className="space-y-4 px-5 py-4">
          {subagents.map((subagent) => (
            <SubagentRow
              key={subagent.id}
              orgId={orgId}
              projectId={projectId}
              subagent={subagent}
              duplicateHandle={(handleCounts.get(subagent.handle) ?? 0) > 1}
              onChange={(fields) => {
                update(subagent.id, fields)
              }}
              onRemove={() => {
                onSubagentsChange(subagents.filter((entry) => entry.id !== subagent.id))
              }}
            />
          ))}
          <Field className="max-w-xs">
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
        </div>
      ) : null}
    </AgentConfigSectionCard>
  )
}

function SubagentRow({
  orgId,
  projectId,
  subagent,
  duplicateHandle,
  onChange,
  onRemove,
}: {
  orgId: string
  projectId: string
  subagent: BasicSubagent
  duplicateHandle: boolean
  onChange: (fields: Partial<BasicSubagent>) => void
  onRemove: () => void
}) {
  const handleError = duplicateHandle
    ? 'Handle must be unique.'
    : subagentHandleError(subagent.handle)
  const fieldId = (name: string) => `agent-config-subagent-${subagent.id}-${name}`
  return (
    <div className="border-border bg-muted/30 space-y-3 rounded-md border p-3">
      <div className="grid gap-3 sm:grid-cols-[1fr_1fr_auto]">
        <Field>
          <FieldLabel htmlFor={fieldId('handle')}>Handle</FieldLabel>
          <Input
            id={fieldId('handle')}
            value={subagent.handle}
            placeholder="researcher"
            aria-invalid={handleError !== undefined}
            onChange={(event) => {
              onChange({ handle: event.target.value.trim() })
            }}
          />
          {handleError !== undefined && subagent.handle !== '' && (
            <FieldError>{handleError}</FieldError>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor={fieldId('type')}>Runs</FieldLabel>
          <Select
            value={subagent.type}
            onValueChange={(value) => {
              onChange({ type: value as SubagentType })
            }}
          >
            <SelectTrigger id={fieldId('type')} className="w-full">
              <SelectValue>{subagentTypeLabels[subagent.type]}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {(Object.keys(subagentTypeLabels) as SubagentType[]).map((type) => (
                <SelectItem key={type} value={type}>
                  {subagentTypeLabels[type]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Button
          type="button"
          size="icon"
          variant="ghost"
          className="self-end"
          aria-label={`Remove subagent ${subagent.handle || 'entry'}`}
          onClick={onRemove}
        >
          <Trash2Icon />
        </Button>
      </div>
      {subagent.type === 'profile' && (
        <Field>
          <FieldLabel htmlFor={fieldId('profile')}>Agent profile</FieldLabel>
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
      <Field>
        <FieldLabel htmlFor={fieldId('description')}>Description</FieldLabel>
        <Input
          id={fieldId('description')}
          value={subagent.description}
          placeholder="What this subagent is for, shown to the model."
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
      <div className="grid gap-3 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={fieldId('max-concurrent')}>Max concurrent</FieldLabel>
          <Input
            id={fieldId('max-concurrent')}
            inputMode="numeric"
            value={subagent.maxConcurrent}
            placeholder="Unlimited"
            onChange={(event) => {
              onChange({ maxConcurrent: event.target.value.trim() })
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
