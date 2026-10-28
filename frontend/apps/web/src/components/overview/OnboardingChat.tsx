import type { Agent, AgentProfileSummary, VisibleProject } from '@omnara/sdk'
import { Link } from '@tanstack/react-router'
import type { ReactNode } from 'react'

import { ArrowUpRight, PlayIcon } from '@/components/icons'
import { CodeTabsBlock, nodeHint, terminalHint } from '@/components/overview/CodeBlock'
import { chatCommands } from '@/components/overview/onboardingCli'
import type { ChatRun } from '@/components/overview/useCreateChat'
import { Button } from '@/components/ui/button'
import { useWebConfig } from '@/lib/web-config'

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
  profile: AgentProfileSummary
  run?: ChatRun
}) {
  const { data: webConfig } = useWebConfig()
  const apiUrl = webConfig?.apiURL ?? `${window.location.origin}/api/v1`
  const commands = chatCommands({
    apiUrl,
    orgId,
    projectId: project.id,
    profileId: profile.id,
    configId: profile.current_config_id,
  })
  return (
    <CodeTabsBlock
      label="How to start a chat"
      tabs={[
        { value: 'cli', label: 'CLI', content: commands.cli, hint: terminalHint },
        { value: 'sdk', label: 'TypeScript SDK', content: commands.sdk, hint: nodeHint },
        { value: 'curl', label: 'cURL', content: commands.curl, hint: terminalHint },
      ]}
      footer={run && <RunFooter pending={run.pending} error={run.error} onRun={run.onRun} />}
    />
  )
}

export function ChatGuide({ project, agent }: { project: VisibleProject; agent: Agent }) {
  return (
    <Button asChild size="sm">
      <Link
        to="/projects/$projectId/agents/$agentId/chat"
        params={{ projectId: project.id, agentId: agent.id }}
        data-action="open-agent"
      >
        Open chat
        <ArrowUpRight />
      </Link>
    </Button>
  )
}
