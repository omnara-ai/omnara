import { useAgentProfiles, useOmnaraClient } from '@omnara/react'
import type { AgentProfile, ProjectApp } from '@omnara/sdk'
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

export function ProjectAppProfilePicker({
  orgId,
  projectId,
  value,
  onChange,
  slotCount,
  disabled,
  single = false,
  label = 'Offered profiles',
  description,
}: {
  orgId: string
  projectId: string
  value: ProfileOption[]
  onChange: (profiles: ProfileOption[]) => void
  slotCount?: number | null
  disabled?: boolean
  single?: boolean
  label?: string
  description?: string
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
      <FieldLabel htmlFor="app-profiles">{label}</FieldLabel>
      {single ? (
        <ProfileCombobox
          id="app-profiles"
          items={items}
          value={selected[0] ?? null}
          onValueChange={(profile) => {
            onChange(profile ? [profile] : [])
          }}
          search={search}
          query={query}
          disabled={disabled}
        />
      ) : (
        <ProfilesCombobox
          id="app-profiles"
          items={items}
          value={selected}
          onValueChange={onChange}
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
              that agent. Up to 16 slots per setup
              {displayedSlotCount === null ? '.' : ` (${displayedSlotCount}/16 selected).`}
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

export function DiscordAppInteractionsSetup({ app }: { app?: ProjectApp }) {
  const client = useOmnaraClient()
  const apiOrigin = new URL(client.getConfig().baseUrl ?? '/api/v1', window.location.origin).origin
  return (
    <p className="text-muted-foreground text-sm">
      Discord needs the application’s public key and a working Interactions Endpoint URL for
      multiple profile choices and agent questions. In the Discord Developer Portal, set the
      Interactions Endpoint URL to{' '}
      <code className="break-all">
        {apiOrigin}/api/integrations/discord/
        {app?.provider_tenant_id === ''
          ? 'APPLICATION_ID'
          : (app?.provider_tenant_id ?? 'APPLICATION_ID')}
        /interactions
      </code>
      . Use this app’s saved public key; changing offered profiles reuses this bot.
    </p>
  )
}
