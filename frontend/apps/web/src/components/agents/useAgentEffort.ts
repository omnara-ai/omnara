import { useUpdateAgentConfig } from '@omnara/react'
import { type Agent, type AgentConfig, ApiError } from '@omnara/sdk'
import { useState } from 'react'

import { withReasoningEffort } from '@/components/agents/agentConfigReasoningEffort'

export type AgentEffort = NonNullable<ReturnType<typeof useAgentEffort>>

function effortErrorMessage(error: Error): string {
  if (error instanceof ApiError && error.status === 409) {
    return 'The agent’s configuration changed. Choose the effort again.'
  }
  return error.message
}

export function useAgentEffort({
  orgId,
  projectId,
  agent,
  config,
  canManage,
}: {
  orgId: string
  projectId: string
  agent: Agent
  config: AgentConfig | undefined
  canManage: boolean
}) {
  const updateConfig = useUpdateAgentConfig(orgId, projectId, agent.id)
  const [attempt, setAttempt] = useState<{ configId: string; effort: string; error?: string }>()
  if (config === undefined) return null
  const { supports_reasoning, supported_reasoning_efforts, default_reasoning_effort } = config.model
  if (!supports_reasoning || supported_reasoning_efforts.length === 0) return null
  const { id: configId, source, source_format: format } = config
  const current = attempt?.configId === configId ? attempt : undefined
  const pending = updateConfig.isPending && current !== undefined

  function change(effort: string) {
    if (source === undefined || format === undefined) return
    const nextSource = withReasoningEffort(source, format, effort)
    if (nextSource === null) {
      setAttempt({ configId, effort, error: 'The agent config could not be updated.' })
      return
    }
    setAttempt({ configId, effort })
    updateConfig.mutate(
      { source: nextSource, source_format: format, expected_current_config_id: configId },
      {
        onError: (err) => {
          setAttempt({ configId, effort, error: effortErrorMessage(err) })
        },
      },
    )
  }

  return {
    options: supported_reasoning_efforts,
    value: pending ? current.effort : default_reasoning_effort,
    editable:
      canManage &&
      agent.state !== 'archived' &&
      source !== undefined &&
      format !== undefined &&
      agent.parent_agent_id === undefined &&
      !pending,
    error: current?.error,
    change,
  }
}
