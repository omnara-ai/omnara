import type { AgentProfile } from '@omnara/sdk'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import {
  createResourceCombobox,
  type ResourceComboboxQuery,
  type ResourceComboboxSearch,
} from '@/components/ui/resource-combobox'

const ProfileCombobox = createResourceCombobox<AgentProfile>({
  itemKey: (profile) => profile.id,
  itemLabel: (profile) => profile.name,
  renderItem: (profile) => (
    <span className="flex min-w-0 flex-col">
      <span className="truncate">{profile.name}</span>
      <span className="text-muted-foreground truncate text-xs">
        {profile.current_config.model.provider_config} · {profile.current_config.model.name}
      </span>
    </span>
  ),
  placeholder: 'Search profiles…',
  emptyMessage: 'No matching profiles.',
})

export function AgentProfileTypeahead({
  profiles,
  selectedProfile,
  search,
  query,
  onSelect,
}: {
  profiles: AgentProfile[]
  selectedProfile: AgentProfile | null
  search: ResourceComboboxSearch
  query: ResourceComboboxQuery
  onSelect: (profile: AgentProfile) => void
}) {
  return (
    <Field>
      <FieldLabel htmlFor="agent-profile-search">Agent profile</FieldLabel>
      <ProfileCombobox
        items={profiles}
        value={selectedProfile}
        onValueChange={(profile) => {
          if (profile) onSelect(profile)
        }}
        search={search}
        query={query}
        placeholder={query.isPending ? 'Loading profiles…' : 'Search profiles…'}
      />
      {selectedProfile && (
        <FieldDescription>
          {selectedProfile.current_config.model.provider_config} ·{' '}
          {selectedProfile.current_config.model.name}
        </FieldDescription>
      )}
    </Field>
  )
}
