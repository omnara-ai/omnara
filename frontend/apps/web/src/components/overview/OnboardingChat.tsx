import { useCreateAgent } from '@omnara/react'
import type { Agent, AgentProfile, VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import { type ReactNode, useEffect, useState } from 'react'

import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { ArrowUpRight, PlayIcon } from '@/components/icons'
import { CodeTabsBlock } from '@/components/overview/CodeBlock'
import { chatCommands, chatMessage } from '@/components/overview/onboardingCli'
import { Button } from '@/components/ui/button'
import { isInsufficientCreditsError } from '@/lib/insufficient-credits'
import { errorMessage } from '@/lib/submit-status'
import { useWebConfig } from '@/lib/web-config'

function useCreateChat(
  orgId: string,
  project: VisibleProject,
  profile: AgentProfile,
  onCreated: (agent: Agent) => void,
) {
  const createAgent = useCreateAgent(orgId, project.id)
  const { data: webConfig } = useWebConfig()
  const [error, setError] = useState<ReactNode>(null)

  async function launch(message?: string) {
    setError(null)
    try {
      const launched = await createAgent.mutateAsync({
        profile: profile.id,
        config: profile.current_config_id,
        message,
      })
      onCreated(launched.agent)
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

export function CreateChat({
  orgId,
  project,
  profile,
  onCreated,
}: {
  orgId: string
  project: VisibleProject
  profile: AgentProfile
  onCreated: (agent: Agent) => void
}) {
  const chat = useCreateChat(orgId, project, profile, onCreated)
  return (
    <div className="flex flex-col gap-4">
      {chat.error && (
        <p className="text-destructive text-sm" role="alert">
          {chat.error}
        </p>
      )}
      <div>
        <Button
          type="button"
          data-action="create-chat"
          disabled={chat.pending}
          loading={chat.pending}
          onClick={() => {
            void chat.launch()
          }}
        >
          Create chat
        </Button>
      </div>
    </div>
  )
}

export function RunFooter({
  pending,
  error,
  onRun,
}: {
  pending: boolean
  error: ReactNode
  onRun: () => void
}) {
  return (
    <div className="flex items-center gap-3">
      {error && (
        <p className="text-destructive max-w-xs truncate text-sm" role="alert">
          {error}
        </p>
      )}
      <Button
        type="button"
        size="sm"
        data-action="run"
        icon={<PlayIcon />}
        disabled={pending}
        onClick={onRun}
      >
        Run
      </Button>
    </div>
  )
}

export function ChatOptions({
  orgId,
  project,
  profile,
  onCreated,
  onPendingChange,
}: {
  orgId: string
  project: VisibleProject
  profile: AgentProfile
  onCreated?: (agent: Agent) => void
  onPendingChange?: (pending: boolean) => void
}) {
  const chat = useCreateChat(orgId, project, profile, onCreated ?? (() => undefined))
  const { data: webConfig } = useWebConfig()
  useEffect(() => {
    onPendingChange?.(chat.pending)
  }, [chat.pending, onPendingChange])
  const commands = chatCommands({
    origin: webConfig?.publicURL ?? window.location.origin,
    orgId,
    projectId: project.id,
    profileId: profile.id,
    configId: profile.current_config_id,
  })
  return (
    <CodeTabsBlock
      label="How to start a chat"
      tabs={[
        { value: 'cli', label: 'CLI', content: commands.cli },
        { value: 'sdk', label: 'TypeScript SDK', content: commands.sdk },
        { value: 'curl', label: 'cURL', content: commands.curl },
      ]}
      footer={
        onCreated && (
          <RunFooter
            pending={chat.pending}
            error={chat.error}
            onRun={() => {
              void chat.launch(chatMessage)
            }}
          />
        )
      }
    />
  )
}

export function ChatGuide({ project, agent }: { project: VisibleProject; agent: Agent }) {
  return (
    <Button asChild size="sm">
      <Link
        to="/projects/$projectId/agents/$agentId"
        params={{ projectId: project.id, agentId: agent.id }}
        data-action="open-agent"
      >
        Open chat
        <ArrowUpRight />
      </Link>
    </Button>
  )
}
