import { useAgentProfiles, useOmnaraClient } from '@omnara/react'
import type { AgentProfile, IntegrationConnection } from '@omnara/sdk'
import { getAgentProfileOptions } from '@omnara/sdk/tanstack'
import { useQueries } from '@tanstack/react-query'

import { Button } from '@/components/ui/button'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
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

export function ProjectAppProfilePicker({
  orgId,
  projectId,
  value,
  onChange,
  slotCount,
  disabled,
}: {
  orgId: string
  projectId: string
  value: ProfileOption[]
  onChange: (profiles: ProfileOption[]) => void
  slotCount?: number | null
  disabled?: boolean
}) {
  const displayedSlotCount = slotCount === undefined ? value.length : slotCount
  const search = useTypeaheadSearch()
  const query = useAgentProfiles(orgId, projectId, {
    filters: search.filters,
    sort: 'name',
    pageSize: 25,
  })
  const items = useInfiniteQueryItems(query)
  const client = useOmnaraClient()
  const selectedQueries = useQueries({
    queries: value.map((profile) => ({
      ...getAgentProfileOptions({
        path: { orgID: orgId, projectID: projectId, agentProfileID: profile.id },
        client,
      }),
      enabled: profile.name === profile.id,
    })),
  })
  // Resolve saved selections independently of the current search/page; never drop failed lookups.
  const selected = value.map(
    (profile, index) =>
      selectedQueries[index]?.data ?? items.find((item) => item.id === profile.id) ?? profile,
  )
  const failedNames = selectedQueries.filter(
    (result, index) => result.isError && selected[index]?.name === value[index]?.id,
  )
  const loadingNames = selectedQueries.some(
    (result, index) => result.isFetching && selected[index]?.name === value[index]?.id,
  )
  return (
    <Field>
      <FieldLabel htmlFor="app-profiles">Offered profiles</FieldLabel>
      <ProfilesCombobox
        id="app-profiles"
        items={items}
        value={selected}
        onValueChange={onChange}
        search={search}
        query={query}
        disabled={disabled}
      />
      <FieldDescription>
        With one eligible profile, a mention launches it immediately. With multiple eligible
        profiles, a native menu asks the person to choose just one. Later messages stay with that
        agent. Up to 16 slots per setup
        {displayedSlotCount === null ? '.' : ` (${displayedSlotCount}/16 selected).`}
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

export function DiscordAppInteractionsSetup({
  connection,
}: {
  connection?: IntegrationConnection
}) {
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  return (
    <p className="text-muted-foreground text-sm">
      Discord needs the application’s public key and a working Interactions Endpoint URL for
      multiple choices, even with agent questions turned off. In the Discord Developer Portal, set
      the Interactions Endpoint URL to{' '}
      <code className="break-all">
        {apiOrigin}/api/integrations/discord/{connection?.id ?? 'CONNECTION_ID'}/interactions
      </code>
      . Use the connection’s saved public key; changing offered profiles reuses this bot.
    </p>
  )
}
