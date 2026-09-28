import type { Skill } from '@omnara/sdk'

import type { ResourceComboboxConfig } from '@/components/ui/resource-combobox-core'

export const skillComboboxConfig: ResourceComboboxConfig<Skill> = {
  itemKey: (skill) => skill.id,
  itemLabel: (skill) => skill.name,
  renderItem: (skill) => (
    <span className="flex min-w-0 flex-col gap-0.5">
      <span className="font-medium">{skill.name}</span>
      <span className="text-muted-foreground line-clamp-2 text-xs">{skill.description}</span>
    </span>
  ),
  placeholder: 'Search skills…',
  emptyMessage: 'No available skills found.',
}
