import {
  useCreateAgent,
  useCreateAgentConfig,
  useCreateAgentProfile,
  useUpdateAgentProfile,
} from '@omnara/react'
import type { AgentConfigErrorIssue, ApiError } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { useRef, useState } from 'react'

import { configSubmitError } from '@/lib/agent-config-issues'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, settleSubmission, submitting } from '@/lib/submit-status'

export type SubmitAction = 'profile' | 'launch'

interface SavedProfile {
  name: string
  yaml: string
  profileId: string
  configId: string
}

export function useCreateAgentSubmission(orgId: string, projectId: string) {
  const createAgentConfig = useCreateAgentConfig(orgId, projectId)
  const createAgentProfile = useCreateAgentProfile(orgId, projectId)
  const updateAgentProfile = useUpdateAgentProfile(orgId, projectId)
  const createAgent = useCreateAgent(orgId, projectId)
  const navigate = useNavigate()
  const [status, setStatus] = useState<SubmitStatus>(idle)
  const [pendingAction, setPendingAction] = useState<SubmitAction | null>(null)
  const [launchError, setLaunchError] = useState<ApiError>()
  const [issues, setIssues] = useState<AgentConfigErrorIssue[]>([])
  const savedProfile = useRef<SavedProfile | null>(null)

  async function submit(name: string, yaml: string, action: SubmitAction) {
    setLaunchError(undefined)
    setIssues([])
    setStatus(submitting)
    setPendingAction(action)
    const result = await settleSubmission(async () => {
      let profile = savedProfile.current
      if (profile?.name !== name || profile.yaml !== yaml) {
        const config = await createAgentConfig.mutateAsync({ source: yaml, source_format: 'yaml' })
        if (profile?.name === name) {
          await updateAgentProfile.mutateAsync({
            agentProfileID: profile.profileId,
            config: config.id,
            expected_current_config_id: profile.configId,
          })
          profile = { ...profile, yaml, configId: config.id }
        } else {
          const created = await createAgentProfile.mutateAsync({ name, config: config.id })
          profile = { name, yaml, profileId: created.id, configId: config.id }
        }
        savedProfile.current = profile
      }
      if (action === 'launch') {
        const launch = await createAgent.mutateAsync({
          profile: profile.profileId,
          config: profile.configId,
        })
        await navigate({
          to: '/projects/$projectId/agents/$agentId',
          params: { projectId, agentId: launch.agent.id },
        })
      } else {
        await navigate({
          to: '/projects/$projectId/agent-profiles/$profileId',
          params: { projectId, profileId: profile.profileId },
        })
      }
    }).finally(() => {
      setPendingAction(null)
    })

    if (result.ok) {
      setStatus(idle)
    } else {
      if (action === 'launch' && isInsufficientCreditsError(result.error)) {
        setLaunchError(result.error)
      }
      const failure = configSubmitError(
        result.error,
        action === 'launch' ? 'Could not create agent' : 'Could not create profile',
      )
      setIssues(failure.issues)
      setStatus({ phase: 'error', message: failure.message })
    }
  }

  return { submit, status, pendingAction, launchError, issues }
}
