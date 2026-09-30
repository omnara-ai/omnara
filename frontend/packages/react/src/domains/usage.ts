import type {
  UsageTimeseriesGroupBy,
  UsageTimeseriesInterval,
  UsageTimeseriesMetric,
} from '@omnara/sdk'
import {
  getAgentProfileUsageOptions,
  getAgentUsageOptions,
  getOrgUsageOptions,
  getProjectUsageOptions,
  getUsageTimeseriesOptions,
} from '@omnara/sdk/tanstack'
import { keepPreviousData, type Query, useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'
import { generatedQueryKey } from './query-keys'

export interface UsageWindow {
  since?: string
  until?: string
}

export interface OrgUsageFilters extends UsageWindow {
  includeProjectIDs?: string[]
  excludeProjectIDs?: string[]
}

export interface AgentProfileUsageFilters extends UsageWindow {
  includeSubagents?: boolean
}

export interface UsageTimeseriesFilters extends UsageWindow {
  timezone?: string
  interval?: UsageTimeseriesInterval
  metric?: UsageTimeseriesMetric
  groupBy?: UsageTimeseriesGroupBy
  groupLimit?: number
  orgIDs?: string[]
  projectIDs?: string[]
  agentProfileIDs?: string[]
  includeSubagents?: boolean
}

function nonEmpty(ids: string[] | undefined) {
  return ids && ids.length > 0 ? ids : undefined
}

export function useOrgUsage(orgID: string, filters: OrgUsageFilters = {}) {
  const client = useOmnaraClient()
  return useQuery({
    ...getOrgUsageOptions({
      path: { orgID },
      query: {
        since: filters.since,
        until: filters.until,
        include_project_ids: nonEmpty(filters.includeProjectIDs),
        exclude_project_ids: nonEmpty(filters.excludeProjectIDs),
      },
      client,
    }),
    placeholderData: keepPreviousData,
  })
}

export function useProjectUsage(orgID: string, projectID: string, window: UsageWindow = {}) {
  const client = useOmnaraClient()
  return useQuery({
    ...getProjectUsageOptions({
      path: { orgID, projectID },
      query: { since: window.since, until: window.until },
      client,
    }),
    placeholderData: keepPreviousData,
  })
}

export function useAgentProfileUsage(
  orgID: string,
  projectID: string,
  agentProfileID: string,
  filters: AgentProfileUsageFilters = {},
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getAgentProfileUsageOptions({
      path: { orgID, projectID, agentProfileID },
      query: {
        since: filters.since,
        until: filters.until,
        include_subagents: filters.includeSubagents ?? false,
      },
      client,
    }),
    placeholderData: keepPreviousData,
  })
}

export function useAgentUsage(
  orgID: string,
  projectID: string,
  agentID: string,
  includeSubagents: boolean,
  window: UsageWindow = {},
) {
  const client = useOmnaraClient()
  return useQuery({
    ...getAgentUsageOptions({
      path: { orgID, projectID, agentID },
      query: { include_subagents: includeSubagents, since: window.since, until: window.until },
      client,
    }),
    placeholderData: keepPreviousData,
  })
}

export function useUsageTimeseries(filters: UsageTimeseriesFilters = {}) {
  const client = useOmnaraClient()
  return useQuery({
    ...getUsageTimeseriesOptions({
      query: {
        since: filters.since,
        until: filters.until,
        timezone: filters.timezone,
        interval: filters.interval,
        metric: filters.metric,
        group_by: filters.groupBy,
        group_limit: filters.groupLimit,
        org_ids: nonEmpty(filters.orgIDs),
        project_ids: nonEmpty(filters.projectIDs),
        agent_profile_ids: nonEmpty(filters.agentProfileIDs),
        include_subagents: filters.includeSubagents,
      },
      client,
    }),
    placeholderData: keepPreviousData,
  })
}

export function agentUsageQueryPredicate(orgID: string, projectID: string) {
  return (query: Query): boolean => {
    const key = generatedQueryKey(query)
    return (
      key?._id === 'getAgentUsage' && key.path?.orgID === orgID && key.path.projectID === projectID
    )
  }
}
