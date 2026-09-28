import { useAgentProfiles, useOmnaraClient } from '@omnara/react'
import type { AgentProfile } from '@omnara/sdk'
import { getAgentProfileOptions } from '@omnara/sdk/tanstack'
import { useQueries } from '@tanstack/react-query'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { createResourceCombobox } from '@/components/ui/resource-combobox'
import { createResourceMultiCombobox } from '@/components/ui/resource-multi-combobox'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { useTypeaheadSearch } from '@/hooks/use-resource-list'

type ProfileOption = Pick<AgentProfile, 'id' | 'name'>

const ProfilesCombobox = createResourceMultiCombobox<ProfileOption>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  placeholder: 'Search agent profiles…',
  emptyMessage: 'No agent profiles found.',
})

const ProfileCombobox = createResourceCombobox<ProfileOption>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  placeholder: 'Choose an agent profile…',
  emptyMessage: 'No agent profiles found.',
})

export function IntegrationProfilePicker({
  orgId,
  projectId,
  value,
  onChange,
  profileCount,
  disabled,
  single = false,
  label = 'Offered profiles',
  description,
}: {
  orgId: string
  projectId: string
  value: string[]
  onChange: (profileIds: string[]) => void
  profileCount?: number | null
  disabled?: boolean
  single?: boolean
  label?: string
  description?: string
}) {
  const displayedProfileCount = profileCount === undefined ? value.length : profileCount
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
      <FieldLabel htmlFor="integration-profiles">{label}</FieldLabel>
      {single ? (
        <ProfileCombobox
          id="integration-profiles"
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
          id="integration-profiles"
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
      <FieldDescription>
        {description ??
          (single ? (
            'Choose one profile to launch for matching GitHub events.'
          ) : (
            <>
              With one eligible profile, a mention launches it immediately. With multiple eligible
              profiles, a native menu asks the person to choose just one. Later messages stay with
              that agent. Up to 16 profiles per setup
              {displayedProfileCount === null ? '.' : ` (${displayedProfileCount}/16 selected).`}
            </>
          ))}
      </FieldDescription>
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
