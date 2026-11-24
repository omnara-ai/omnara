import type { Agent, CurrentUserOrg, ProjectModelGrantListItem, VisibleProject } from '@omnara/sdk'

import { createResourceCombobox } from '@/components/ui/resource-combobox'

export const OrgPicker = createResourceCombobox<CurrentUserOrg>({
  itemKey: (org) => org.id,
  itemLabel: (org) => org.name,
  placeholder: 'Choose organization',
})
export const ProjectPicker = createResourceCombobox<VisibleProject>({
  itemKey: (project) => project.id,
  itemLabel: (project) => project.name,
  placeholder: 'Choose project',
})
export const AgentPicker = createResourceCombobox<Agent>({
  itemKey: (agent) => agent.id,
  itemLabel: (agent) => agent.name || agent.id,
  placeholder: 'Choose agent',
})
export const ModelPicker = createResourceCombobox<ProjectModelGrantListItem>({
  itemKey: (item) => item.model.id,
  itemLabel: (item) => `${item.model.provider_config}/${item.model.name}`,
  placeholder: 'Choose model',
})
