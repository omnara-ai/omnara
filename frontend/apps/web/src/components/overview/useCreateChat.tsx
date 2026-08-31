import { useCreateAgent } from '@omnara/react'
import type { AgentProfile, VisibleProject } from '@omnara/sdk'
import { type ReactNode, useState } from 'react'

import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import { errorMessage } from '@/lib/submit-status'
import { useWebConfig } from '@/lib/web-config'

export interface ChatRun {
  pending: boolean
  error: ReactNode
  onRun: () => void
}

export function useCreateChat(
  orgId: string,
  project: VisibleProject,
  profile: AgentProfile | undefined,
) {
  const createAgent = useCreateAgent(orgId, project.id)
  const { data: webConfig } = useWebConfig()
  const [error, setError] = useState<ReactNode>(null)

  async function launch(message?: string) {
    if (!profile) return
    setError(null)
    try {
      await createAgent.mutateAsync({
        profile: profile.id,
        config: profile.current_config_id,
        message,
      })
    } catch (err) {
      setError(
        isInsufficientCreditsError(err) && webConfig?.billingURL ? (
          <InsufficientCreditsMessage billingHref={webConfig.billingHref} />
        ) : (
          errorMessage(err, 'Could not create chat')
        ),
      )
    }
  }

  return { launch, pending: createAgent.isPending, error }
}
