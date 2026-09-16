import type { VisibleProject } from '@omnara/sdk'

export type UsageProjectFilterValue = VisibleProject[]

export const allProjectsUsageFilter: UsageProjectFilterValue = []

const inlineProjectNameLimit = 2

export function usageProjectFilterLabel(value: UsageProjectFilterValue) {
  if (value.length === 0) return 'All projects'
  if (value.length <= inlineProjectNameLimit) return value.map((project) => project.name).join(', ')
  return `${value.length} projects`
}
