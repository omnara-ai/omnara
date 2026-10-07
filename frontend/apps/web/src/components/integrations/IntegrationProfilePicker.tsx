import { useAgentProfiles, useOmnaraClient } from '@omnara/react'
import type { AgentProfile } from '@omnara/sdk'
import { getAgentProfileOptions } from '@omnara/sdk/tanstack'
import { useQueries } from '@tanstack/react-query'
import { useId } from 'react'

import { AgentIcon } from '@/components/agents/AgentIcon'
import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'
import { profileIcon } from '@/lib/agent-icon'

type ProfileOption = Pick<AgentProfile, 'id' | 'name'>

const ProfilesCombobox = createResourceMultiCombobox<ProfileOption>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  renderItem: profileLabel,
  renderValue: profileLabel,
  placeholder: 'Search agent profiles…',
  emptyMessage: 'No agent profiles found.',
})

const ProfileCombobox = createResourceCombobox<ProfileOption>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  renderItem: profileLabel,
  renderValue: profileLabel,
  placeholder: 'Choose an agent profile…',
  emptyMessage: 'No agent profiles found.',
})

export function IntegrationProfilePicker({
  orgId,
  projectId,
  value,
  onChange,
  disabled,
  single = false,
  label = 'Offered profiles',
  description,
}: {
  orgId: string
  projectId: string
  value: string[]
  onChange: (profileIds: string[]) => void
  disabled?: boolean
  single?: boolean
  label?: string
  description?: string | null
}) {
  const id = useId()
  const search = useTypeaheadSearch()
  const query = useAgentProfiles(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const items = useInfiniteQueryItems(query)
  const client = useOmnaraClient()
  const selectedQueries = useQueries({
    queries: value.map((id) => ({
      ...getAgentProfileOptions({
        path: { orgID: orgId, projectID: projectId, agentProfileID: id },
        client,
      }),
      enabled: true,
    })),
  })
  const selected = value.map(
    (id, index) =>
      selectedQueries[index]?.data ?? items.find((item) => item.id === id) ?? { id, name: id },
  )
  const failedNames = selectedQueries.filter(
    (result, index) => result.isError && selected[index]?.name === value[index],
  )
  const loadingNames = selectedQueries.some(
    (result, index) => result.isFetching && selected[index]?.name === value[index],
  )
  return (
    <Field>
      <FieldLabel htmlFor={id}>{label}</FieldLabel>
      {single ? (
        <ProfileCombobox
          id={id}
          items={items}
          value={selected[0] ?? null}
          onValueChange={(profile) => {
            onChange(profile ? [profile.id] : [])
          }}
          search={search}
          query={query}
          disabled={disabled}
        />
      ) : (
        <ProfilesCombobox
          id={id}
          items={items}
          value={selected}
          onValueChange={(profiles) => {
            onChange(profiles.map((profile) => profile.id))
          }}
          search={search}
          query={query}
          disabled={disabled}
        />
      )}
      {description && <FieldDescription>{description}</FieldDescription>}
      {!single && value.length >= 16 && (
        <FieldDescription>Up to 16 profiles can be offered.</FieldDescription>
      )}
      {loadingNames && (
        <p role="status" className="text-muted-foreground text-sm">
          Loading saved profile names…
        </p>
      )}
      {failedNames.length > 0 && (
        <div role="alert" className="text-sm">
          Some saved profile names could not be loaded. Their IDs are shown and their selections are
          kept.
          <Button
            type="button"
            variant="link"
            disabled={loadingNames}
            onClick={() => {
              void Promise.all(failedNames.map((result) => result.refetch()))
            }}
          >
            Retry profile names
          </Button>
        </div>
      )}
    </Field>
  )
}

function profileLabel(profile: ProfileOption) {
  return (
    <span className="flex min-w-0 items-center gap-2">
      <AgentIcon icon={profileIcon(profile.id)} className="size-5 rounded-[2px]" />
      <span className="truncate">{profile.name}</span>
    </span>
  )
}
