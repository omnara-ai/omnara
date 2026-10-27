import { useOrgOverview, usePersonalAccessTokens } from '@omnara/react'
import {
  type Agent,
  type AgentProfileSummary,
  isCliLoginToken,
  type OrgOverviewResponse,
  type PersonalAccessToken,
} from '@omnara/sdk'
import { useDeferredValue } from 'react'

import type { StepStatus } from '@/components/overview/OnboardingSteps'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

const pollIntervalMs = 3000
const tokenPageSize = 50

export interface OnboardingProgress {
  ready: boolean
  cliToken?: PersonalAccessToken
  agentProfile?: AgentProfileSummary
  agent?: Agent
  live: { agentProfile?: AgentProfileSummary; agent?: Agent }
  steps: {
    cli: { login: StepStatus; createProfile: StepStatus; chat: StepStatus }
  }
}

function stepStatus(completed: boolean, open: boolean): StepStatus {
  if (completed) return 'done'
  return open ? 'active' : 'upcoming'
}

function projectProgress(overview: OrgOverviewResponse | undefined, projectId: string) {
  const agentProfile = overview?.recent_agent_profiles.find(
    (candidate) => candidate.project_id === projectId,
  )
  const agents = overview?.recent_agents.filter((agent) => agent.project_id === projectId) ?? []
  const agent =
    agents.find((candidate) => candidate.agent_profile_id === agentProfile?.id) ?? agents[0]
  return { agentProfile, agent }
}

/** Polls the overview until an agent shows up in the project, and returns it. */
export function useProjectAgent(input: { orgId: string; projectId: string }) {
  const overviewQuery = useOrgOverview(input.orgId, {
    refetchInterval: (overview) =>
      projectProgress(overview, input.projectId).agent == null ? pollIntervalMs : false,
  })
  return projectProgress(overviewQuery.data, input.projectId).agent
}

export function useOnboarding(input: { orgId: string; projectId: string }): OnboardingProgress {
  const overviewQuery = useOrgOverview(input.orgId, {
    refetchInterval: (overview) =>
      projectProgress(overview, input.projectId).agent == null ? pollIntervalMs : false,
  })
  const live = projectProgress(overviewQuery.data, input.projectId)
  const tokensQuery = usePersonalAccessTokens(tokenPageSize, {
    refetchInterval: live.agentProfile == null ? pollIntervalMs : false,
  })

  const shownOverview = useDeferredValue(overviewQuery.data)
  const shownTokens = useDeferredValue(tokensQuery.data)
  const cliToken = useInfiniteQueryItems({ data: shownTokens }).find(isCliLoginToken)

  const { agentProfile, agent } = projectProgress(shownOverview, input.projectId)
  const hasProfile = agentProfile != null

  return {
    ready: !tokensQuery.isPending,
    cliToken,
    agentProfile,
    agent,
    live,
    steps: {
      cli: {
        login: stepStatus(cliToken != null || hasProfile, true),
        // Open alongside login: only the CLI command needs it, and Run creates the
        // profile with the browser session.
        createProfile: stepStatus(hasProfile, true),
        chat: stepStatus(agent != null, hasProfile),
      },
    },
  }
}
