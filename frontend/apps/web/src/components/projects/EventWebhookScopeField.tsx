import { useAgentProfiles, useAgents } from '@omnara/react'
import type { EventWebhookScope } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { useId, useState } from 'react'

import { ProfileReferenceLink } from '@/components/agents/AgentReferenceLinks'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

const selectionLabels = {
  project: 'All agents in this project',
  profile: 'Agents using one profile',
  agent: 'One agent',
}
interface Resource {
  id: string
  name: string
}
const ResourcePicker = createResourceCombobox<Resource>({
  itemKey: (item) => item.id,
  itemLabel: (item) => item.name,
  placeholder: 'Choose a resource…',
})

interface ProjectProps {
  orgId: string
  projectId: string
}

export function EventWebhookScopeField({
  orgId,
  projectId,
  value,
  onChange,
  initialSelection,
}: ProjectProps & {
  initialSelection?: Resource
  value: EventWebhookScope
  onChange: (value: EventWebhookScope) => void
}) {
  const id = useId()
  const [selection, setSelection] = useState(initialSelection)
  return (
    <>
      <Field>
        <FieldLabel htmlFor={id}>Receive events from</FieldLabel>
        <Select
          value={value.type}
          onValueChange={(type) => {
            setSelection(undefined)
            if (type === 'project') onChange({ type })
            if (type === 'profile') onChange({ type, agent_profile_id: '' })
            if (type === 'agent') onChange({ type, agent_id: '' })
          }}
        >
          <SelectTrigger id={id}>
            <SelectValue>{selectionLabels[value.type]}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="project">{selectionLabels.project}</SelectItem>
            <SelectItem value="profile">{selectionLabels.profile}</SelectItem>
            <SelectItem value="agent">{selectionLabels.agent}</SelectItem>
          </SelectContent>
        </Select>
        <FieldDescription>
          {value.type === 'agent'
            ? 'Only this agent, not its descendants.'
            : value.type === 'profile'
              ? 'Existing and future agents using the selected profile.'
              : 'Existing and future agents in this project, including subagents.'}
        </FieldDescription>
      </Field>
      {value.type === 'profile' && (
        <ProfilePicker
          orgId={orgId}
          projectId={projectId}
          value={value.agent_profile_id}
          selection={selection}
          onChange={(item) => {
            setSelection(item ?? undefined)
            onChange({ type: 'profile', agent_profile_id: item?.id ?? '' })
          }}
        />
      )}
      {value.type === 'agent' && (
        <AgentPicker
          orgId={orgId}
          projectId={projectId}
          value={value.agent_id}
          selection={selection}
          onChange={(item) => {
            setSelection(item ?? undefined)
            onChange({ type: 'agent', agent_id: item?.id ?? '' })
          }}
        />
      )}
    </>
  )
}

function ProfilePicker({
  orgId,
  projectId,
  value,
  selection,
  onChange,
}: ProjectProps & {
  value: string
  selection?: Resource
  onChange: (value: Resource | null) => void
}) {
  const id = useId()
  const search = useTypeaheadSearch()
  const [open, setOpen] = useState(false)
  const query = useAgentProfiles(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
    enabled: open,
  })
  const items = useInfiniteQueryItems(query)
  return (
    <Field>
      <FieldLabel htmlFor={id}>Agent profile</FieldLabel>
      <ResourcePicker
        id={id}
        items={items}
        query={query}
        search={search}
        onOpenChange={setOpen}
        value={
          value
            ? (items.find((item) => item.id === value) ??
              (selection?.id === value ? selection : { id: value, name: value }))
            : null
        }
        onValueChange={onChange}
        placeholder="Choose an agent profile…"
      />
    </Field>
  )
}

function AgentPicker({
  orgId,
  projectId,
  value,
  selection,
  onChange,
}: ProjectProps & {
  value: string
  selection?: Resource
  onChange: (value: Resource | null) => void
}) {
  const id = useId()
  const search = useTypeaheadSearch()
  const [open, setOpen] = useState(false)
  const query = useAgents(orgId, projectId, {
    filters: { ...search.filters, include_subagents: true },
    sort: 'name',
    pageSize: 25,
    enabled: open,
    poll: false,
  })
  const items = useInfiniteQueryItems(query).map((agent) => ({
    id: agent.id,
    name: agent.name || agent.id,
  }))
  return (
    <Field>
      <FieldLabel htmlFor={id}>Agent</FieldLabel>
      <ResourcePicker
        id={id}
        items={items}
        query={query}
        search={search}
        onOpenChange={setOpen}
        value={
          value
            ? (items.find((item) => item.id === value) ??
              (selection?.id === value ? selection : { id: value, name: value }))
            : null
        }
        onValueChange={onChange}
        placeholder="Choose an agent…"
      />
    </Field>
  )
}

export function EventWebhookScopeLabel({
  projectId,
  scope,
  name,
}: {
  projectId: string
  scope: EventWebhookScope
  name?: string
}) {
  if (scope.type === 'project') return <>All agents in this project</>
  if (scope.type === 'profile')
    return (
      <span className="flex items-center gap-1">
        Agents using profile:{' '}
        <ProfileReferenceLink
          projectId={projectId}
          profileId={scope.agent_profile_id}
          name={name ?? scope.agent_profile_id}
        />
      </span>
    )
  return (
    <span className="flex items-center gap-1">
      Agent:{' '}
      <Link
        to="/projects/$projectId/agents/$agentId"
        params={{ projectId, agentId: scope.agent_id }}
        className="flex min-w-0 items-center gap-1.5 hover:underline"
      >
        <span className="truncate">{name ?? scope.agent_id}</span>
      </Link>
    </span>
  )
}
