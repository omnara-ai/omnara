import type { Agent, AgentProfile, VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { ArrowUpRight, PlayIcon } from '@/components/icons'
import { CodeTabsBlock } from '@/components/overview/CodeBlock'
import { chatCommands } from '@/components/overview/onboardingCli'
import { type ChatRun, useCreateChat } from '@/components/overview/useCreateChat'
import { Button } from '@/components/ui/button'
import { useWebConfig } from '@/lib/web-config'

export function CreateChat({
  orgId,
  project,
  profile,
}: {
  orgId: string
  project: VisibleProject
  profile: AgentProfile
}) {
  const chat = useCreateChat(orgId, project, profile)
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
  run,
}: {
  orgId: string
  project: VisibleProject
  profile: AgentProfile
  run?: ChatRun
}) {
  const { data: webConfig } = useWebConfig()
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
      footer={run && <RunFooter pending={run.pending} error={run.error} onRun={run.onRun} />}
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
