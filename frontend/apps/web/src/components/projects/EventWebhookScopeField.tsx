import { useAgentProfileQuery, useAgentProfiles, useAgentQuery, useAgents } from '@omnara/react'
import type { EventWebhookScope } from '@omnara/sdk'
import { useId } from 'react'

import {
  FetchedAgentReferenceLink,
  ProfileReferenceLink,
} from '@/components/agents/AgentReferenceLinks'
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
const ResourcePicker = createResourceCombobox<{ id: string; name: string }>({
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
}: ProjectProps & {
  value: EventWebhookScope
  onChange: (value: EventWebhookScope) => void
}) {
  const id = useId()
  return (
    <>
      <Field>
        <FieldLabel htmlFor={id}>Receive events from</FieldLabel>
        <Select
          value={value.type}
          onValueChange={(type) => {
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
          onChange={(agent_profile_id) => {
            onChange({ type: 'profile', agent_profile_id })
          }}
        />
      )}
      {value.type === 'agent' && (
        <AgentPicker
          orgId={orgId}
          projectId={projectId}
          value={value.agent_id}
          onChange={(agent_id) => {
            onChange({ type: 'agent', agent_id })
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
  onChange,
}: ProjectProps & {
  value: string
  onChange: (value: string) => void
}) {
  const id = useId()
  const search = useTypeaheadSearch()
  const query = useAgentProfiles(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const items = useInfiniteQueryItems(query)
  const selected = useAgentProfileQuery(orgId, projectId, value || undefined)
  return (
    <Field>
      <FieldLabel htmlFor={id}>Agent profile</FieldLabel>
      <ResourcePicker
        id={id}
        items={items}
        query={query}
        search={search}
        value={
          value
            ? (selected.data ??
              items.find((item) => item.id === value) ?? { id: value, name: value })
            : null
        }
        onValueChange={(item) => {
          onChange(item?.id ?? '')
        }}
        placeholder="Choose an agent profile…"
      />
    </Field>
  )
}

function AgentPicker({
  orgId,
  projectId,
  value,
  onChange,
}: ProjectProps & {
  value: string
  onChange: (value: string) => void
}) {
  const id = useId()
  const search = useTypeaheadSearch()
  const query = useAgents(orgId, projectId, {
    filters: { ...search.filters, include_subagents: true },
    sort: 'name',
    pageSize: 25,
  })
  const items = useInfiniteQueryItems(query).map((agent) => ({
    id: agent.id,
    name: agent.name || agent.id,
  }))
  const selected = useAgentQuery(orgId, projectId, value || undefined)
  return (
    <Field>
      <FieldLabel htmlFor={id}>Agent</FieldLabel>
      <ResourcePicker
        id={id}
        items={items}
        query={query}
        search={search}
        value={
          value
            ? {
                id: value,
                name:
                  (selected.data?.agent.name ?? '') ||
                  (items.find((item) => item.id === value)?.name ?? value),
              }
            : null
        }
        onValueChange={(item) => {
          onChange(item?.id ?? '')
        }}
        placeholder="Choose an agent…"
      />
    </Field>
  )
}

export function EventWebhookScopeLabel({
  orgId,
  projectId,
  scope,
}: ProjectProps & { scope: EventWebhookScope }) {
  if (scope.type === 'project') return <>All agents in this project</>
  if (scope.type === 'profile')
    return <ProfileLabel orgId={orgId} projectId={projectId} profileId={scope.agent_profile_id} />
  return (
    <span className="flex items-center gap-1">
      Agent:{' '}
      <FetchedAgentReferenceLink orgId={orgId} projectId={projectId} agentId={scope.agent_id} />
    </span>
  )
}

function ProfileLabel({ orgId, projectId, profileId }: ProjectProps & { profileId: string }) {
  const { data } = useAgentProfileQuery(orgId, projectId, profileId)
  return (
    <span className="flex items-center gap-1">
      Agents using profile:{' '}
      <ProfileReferenceLink
        projectId={projectId}
        profileId={profileId}
        name={data?.name ?? profileId}
      />
    </span>
  )
}
