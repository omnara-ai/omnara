import { useOrgOverview, usePersonalAccessTokens } from '@omnara/react'
import {
  type Agent,
  type AgentProfile,
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
  agentProfile?: AgentProfile
  agent?: Agent
  live: { agentProfile?: AgentProfile; agent?: Agent }
  steps: {
    cli: { login: StepStatus; createProfile: StepStatus; chat: StepStatus }
    browser: { createProfile: StepStatus; chat: StepStatus }
  }
}

function statusAt(index: number, completed: boolean[]): StepStatus {
  if (completed[index]) return 'done'
  return completed.indexOf(false) === index ? 'active' : 'upcoming'
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

export function useOnboarding(input: { orgId: string; projectId: string }): OnboardingProgress {
  const overviewQuery = useOrgOverview(input.orgId, {
    refetchInterval: (query) =>
      projectProgress(query.state.data, input.projectId).agent == null ? pollIntervalMs : false,
  })
  const live = projectProgress(overviewQuery.data, input.projectId)
  const tokensQuery = usePersonalAccessTokens(tokenPageSize, {
    refetchInterval: live.agentProfile == null ? pollIntervalMs : false,
  })

  const shownOverview = useDeferredValue(overviewQuery.data)
  const shownTokens = useDeferredValue(tokensQuery.data)
  const cliToken = useInfiniteQueryItems({ data: shownTokens }).find(isCliLoginToken)

  const { agentProfile, agent } = projectProgress(shownOverview, input.projectId)
  const cliCompleted = [
    cliToken != null || agentProfile != null,
    agentProfile != null,
    agent != null,
  ]
  const browserCompleted = [agentProfile != null, agent != null]

  return {
    ready: !tokensQuery.isPending,
    cliToken,
    agentProfile,
    agent,
    live,
    steps: {
      cli: {
        login: statusAt(0, cliCompleted),
        createProfile: statusAt(1, cliCompleted),
        chat: statusAt(2, cliCompleted),
      },
      browser: {
        createProfile: statusAt(0, browserCompleted),
        chat: statusAt(1, browserCompleted),
      },
    },
  }
}
