import type { UseAgentChatResult } from '@omnara/react'
import type { AgentEvent } from '@omnara/sdk'
import { Suspense } from 'react'

import { DataTable, type DataTableColumn } from '@/components/data-table/DataTable'
import { ArrowUpRight } from '@/components/icons'
import { CodeTabsBlock, CopyButton } from '@/components/overview/CodeBlock'
import { Highlighted } from '@/components/overview/highlight'
import { inputCommands } from '@/components/overview/onboardingCli'
import { Button } from '@/components/ui/button'
import { useScrollEdges } from '@/hooks/use-scroll-edges'
import { docsUrl, guides } from '@/lib/docs'
import { formatTime } from '@/lib/format'
import { cn } from '@/lib/utils'
import { useWebConfig } from '@/lib/web-config'

const kindClass: Record<AgentEvent['event_kind'], string> = {
  agent_input: 'text-sky-700 dark:text-sky-300',
  model_output: 'text-violet-700 dark:text-violet-300',
  tool_result: 'text-amber-700 dark:text-amber-300',
  context_checkpoint: 'text-muted-foreground',
}

function eventKindDetail(event: AgentEvent): string | undefined {
  switch (event.event_kind) {
    case 'agent_input':
      if (event.input_kind === 'control') return event.control_type ?? 'control'
      return event.input_kind === 'content' ? undefined : event.input_kind
    case 'model_output':
      return event.stop_reason
    case 'tool_result':
      return event.outcome
    case 'context_checkpoint':
      return undefined
  }
}

const columns: readonly DataTableColumn<AgentEvent>[] = [
  {
    id: 'sequence',
    header: '#',
    className: 'w-20',
    cell: (event) => (
      <span className="text-muted-foreground type-code tabular-nums">{event.sequence}</span>
    ),
  },
  {
    id: 'time',
    header: 'Time',
    className: 'w-28',
    cell: (event) => (
      <span className="text-muted-foreground type-code tabular-nums">
        {formatTime(event.created_at)}
      </span>
    ),
  },
  {
    id: 'kind',
    header: 'Kind',
    className: 'w-44',
    cell: (event) => (
      <span className={cn('font-semibold', kindClass[event.event_kind])}>{event.event_kind}</span>
    ),
  },
  {
    id: 'detail',
    header: 'Detail',
    cell: (event) => (
      <span className="text-muted-foreground type-code">{eventKindDetail(event)}</span>
    ),
  },
]

function EventJson({ event }: { event: AgentEvent }) {
  const json = JSON.stringify(event, null, 2)
  return (
    <div className="relative">
      <div className="absolute right-1 top-1">
        <CopyButton text={json} label="event JSON" />
      </div>
      <pre className="code-highlight type-code whitespace-pre-wrap break-words pr-12">
        <Suspense fallback={json}>
          <Highlighted code={json} language="json" />
        </Suspense>
      </pre>
    </div>
  )
}

function isOpeningConfigChange(event: AgentEvent) {
  return event.is_opening_event && event.input_kind === 'config_change'
}

function SendInputGuide({
  orgId,
  projectId,
  agentId,
}: {
  orgId: string
  projectId: string
  agentId: string
}) {
  const { data: webConfig } = useWebConfig()
  const apiUrl = webConfig?.apiURL ?? `${window.location.origin}/api/v1`
  const commands = inputCommands({ apiUrl, orgId, projectId, agentId })
  return (
    <div className="mx-auto flex w-full max-w-3xl flex-col gap-4">
      <p className="text-muted-foreground text-center text-[15px] font-medium">
        Send a message to interact with your agent
      </p>
      <CodeTabsBlock
        label="How to send input to this agent"
        tabs={[
          { value: 'cli', label: 'CLI', content: commands.cli },
          { value: 'sdk', label: 'TypeScript SDK', content: commands.sdk },
          { value: 'curl', label: 'cURL', content: commands.curl },
        ]}
      />
      <a
        href={docsUrl(guides.agents)}
        target="_blank"
        rel="noreferrer"
        className="text-muted-foreground hover:text-foreground inline-flex items-center gap-1 self-end text-sm transition-colors"
      >
        Documentation
        <ArrowUpRight className="size-3.5" aria-hidden="true" />
      </a>
    </div>
  )
}

export function AgentEventLog({
  chat,
  orgId,
  projectId,
  agentId,
}: {
  chat: UseAgentChatResult
  orgId: string
  projectId: string
  agentId: string
}) {
  const events = [...chat.events].reverse()
  const scrollRef = useScrollEdges()
  const showSendInputGuide =
    chat.historyStatus === 'success' && events.every(isOpeningConfigChange) && events.length <= 1
  return (
    <div ref={scrollRef} className="message-scroll-fade h-full overflow-y-auto">
      <div
        className={cn(
          'mx-auto w-full max-w-5xl px-4 pb-6 pt-2 sm:px-6',
          showSendInputGuide && 'flex min-h-full flex-col justify-center',
        )}
      >
        {showSendInputGuide ? (
          <SendInputGuide orgId={orgId} projectId={projectId} agentId={agentId} />
        ) : (
          <DataTable
            columns={columns}
            data={events}
            getRowId={(event) => event.id}
            rowExpanded={(event) => <EventJson event={event} />}
            isPending={chat.historyStatus === 'pending'}
            isError={chat.historyStatus === 'error'}
            onRetry={chat.retryHistory}
            emptyMessage="No events yet."
          />
        )}
        {chat.hasOlderMessages && (
          <div className="flex justify-center pt-4">
            <Button
              variant="outline"
              size="sm"
              disabled={chat.isLoadingOlderMessages}
              loading={chat.isLoadingOlderMessages}
              onClick={chat.loadOlderMessages}
            >
              Load earlier events
            </Button>
          </div>
        )}
      </div>
    </div>
  )
}
