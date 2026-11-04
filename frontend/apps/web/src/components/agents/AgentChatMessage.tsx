import { type OmnaraUIMessage, useOmnaraClient, useProgramToolCalls } from '@omnara/react'
import type { ToolCall } from '@omnara/sdk'
import { getActorOptions } from '@omnara/sdk/tanstack'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { Streamdown } from 'streamdown'

import { AgentArtifactCard } from '@/components/agents/AgentArtifactCard'
import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import {
  Brain,
  Check,
  ChevronRight,
  CircleDashed,
  CodeBracket,
  HandRaisedIcon,
  Terminal,
  XCircleIcon,
} from '@/components/icons'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Message, MessageContent, MessageHeader } from '@/components/ui/message'
import {
  isInsufficientCreditsModelError,
  isInsufficientCreditsToolError,
} from '@/lib/insufficient-credits'
import { useWebConfig } from '@/lib/web-config'

function ActorLabel({
  actorID,
  orgID,
  projectID,
}: {
  actorID: string
  orgID: string
  projectID: string
}) {
  const client = useOmnaraClient()
  const { data: actor } = useQuery(getActorOptions({ path: { orgID, projectID, actorID }, client }))
  if (actor == null) return null

  const displayName = actor.display_name?.trim()
  const sourceLabel =
    actor.provider === 'integration' ? (actor.metadata.source_label?.trim() ?? '') : ''
  const provider =
    sourceLabel === ''
      ? {
          omnara: 'Omnara',
          integration: 'Integration',
          external: 'External',
        }[actor.provider]
      : sourceLabel
  return (
    <MessageHeader>
      {displayName == null || displayName === '' ? actor.provider_user_id : displayName}
      {` · ${provider}`}
    </MessageHeader>
  )
}

function JsonValue({ value }: { value: unknown }) {
  return (
    <pre className="bg-background/60 type-code max-h-72 overflow-auto rounded-md border p-3">
      {JSON.stringify(value, null, 2)}
    </pre>
  )
}

function AssistantMarkdown({ children }: { children: string }) {
  return (
    <Streamdown
      className="type-body-small leading-prose max-w-3xl py-0.5 text-pretty"
      components={{
        a: ({ children: linkChildren, ...props }) => (
          <a {...props} target="_blank" rel="noreferrer">
            {linkChildren}
          </a>
        ),
      }}
    >
      {children}
    </Streamdown>
  )
}

function AgentConfigDivider({ action }: { action: 'initialized' | 'changed' }) {
  const label =
    action === 'initialized' ? 'Agent configuration initialized' : 'Agent configuration changed'
  return (
    <div className="flex w-full items-center gap-3 py-1" role="separator" aria-label={label}>
      <span className="bg-border h-px flex-1" aria-hidden="true" />
      <span className="text-muted-foreground tracking-eyebrow text-xs font-medium">{label}</span>
      <span className="bg-border h-px flex-1" aria-hidden="true" />
    </div>
  )
}

type ToolUIPart = Extract<OmnaraUIMessage['parts'][number], { type: 'dynamic-tool' }>

interface ToolCallScope {
  orgID: string
  projectID: string
  agentID: string
}

function ToolPart({ part, scope }: { part: ToolUIPart; scope: ToolCallScope }) {
  const [open, setOpen] = useState(false)
  const complete = part.state === 'output-available'
  // Commands can call the agent's tools with `omnarad call`; those calls are
  // fetched while the command runs or while its details are open.
  const programCalls = useProgramToolCalls(
    scope.orgID,
    scope.projectID,
    scope.agentID,
    part.toolCallId,
    { enabled: part.toolName === 'run_command' && (open || part.state === 'input-available') },
  )
  const children = programCalls.data?.pages.flatMap((page) => page.data) ?? []
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="group/tool bg-muted/35 overflow-hidden rounded-lg border"
    >
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs font-medium">
        <Terminal className="text-muted-foreground size-3.5" />
        <span className="min-w-0 flex-1 truncate">{part.toolName}</span>
        {children.length > 0 && (
          <span className="text-muted-foreground shrink-0 font-normal tabular-nums">
            {children.length === 1 ? '1 tool call' : `${String(children.length)} tool calls`}
            {programCalls.hasNextPage ? '+' : ''}
          </span>
        )}
        {complete ? (
          <Check className="text-muted-foreground size-3.5" />
        ) : (
          <CircleDashed className="text-primary size-3.5 motion-safe:animate-spin" />
        )}
        <ChevronRight className="text-muted-foreground size-3.5 transition-transform group-data-[state=open]/tool:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-2 border-t p-2">
        <p className="text-muted-foreground px-1 text-xs font-medium">Input</p>
        <JsonValue value={part.input} />
        {children.length > 0 && (
          <>
            <p className="text-muted-foreground px-1 pt-1 text-xs font-medium">
              Tool calls from this command
            </p>
            <ul className="space-y-1" aria-label="Tool calls from this command">
              {children.map((call) => (
                <li key={call.id}>
                  <ProgramToolCallRow call={call} />
                </li>
              ))}
            </ul>
            {programCalls.hasNextPage && (
              <button
                type="button"
                className="text-muted-foreground hover:text-foreground px-1 text-xs underline"
                disabled={programCalls.isFetchingNextPage}
                onClick={() => void programCalls.fetchNextPage()}
              >
                Show more tool calls
              </button>
            )}
          </>
        )}
        {part.state === 'output-available' && (
          <>
            <p className="text-muted-foreground px-1 pt-1 text-xs font-medium">Output</p>
            <JsonValue value={part.output} />
          </>
        )}
        {part.state === 'output-error' && (
          <p className="text-destructive text-xs">{part.errorText}</p>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

function programToolCallStatus(call: ToolCall) {
  if (call.state === 'awaiting_permission' || call.state === 'awaiting_authorization') {
    return {
      label: call.state === 'awaiting_permission' ? 'Awaiting approval' : 'Awaiting authorization',
      icon: <HandRaisedIcon className="text-primary size-3.5" />,
    }
  }
  if (call.state !== 'completed') {
    return {
      label: 'Running',
      icon: <CircleDashed className="text-primary size-3.5 motion-safe:animate-spin" />,
    }
  }
  if (call.outcome === 'succeeded') {
    return { label: 'Succeeded', icon: <Check className="text-muted-foreground size-3.5" /> }
  }
  return {
    label: call.outcome === undefined ? 'Completed' : capitalize(call.outcome),
    icon: <XCircleIcon className="text-destructive size-3.5" />,
  }
}

function capitalize(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

/** One `omnarad call` a command made. Its result went to the command, not the model. */
function ProgramToolCallRow({ call }: { call: ToolCall }) {
  const status = programToolCallStatus(call)
  return (
    <Collapsible className="group/program-call bg-background/60 rounded-md border">
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-2.5 py-1.5 text-left text-xs">
        <CodeBracket className="text-muted-foreground size-3.5" />
        <span className="min-w-0 flex-1 truncate font-medium">{call.name}</span>
        <span className="text-muted-foreground shrink-0">{status.label}</span>
        <span aria-hidden="true">{status.icon}</span>
        <ChevronRight className="text-muted-foreground size-3.5 transition-transform group-data-[state=open]/program-call:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent className="border-t p-2">
        <JsonValue value={call.input} />
      </CollapsibleContent>
    </Collapsible>
  )
}

export function AgentChatMessage({
  message,
  currentActorId,
  orgID,
  projectID,
  agentID,
}: {
  message: OmnaraUIMessage
  currentActorId?: string
  orgID: string
  projectID: string
  agentID: string
}) {
  const { data: webConfig } = useWebConfig()
  const metadata = message.metadata
  const mine =
    message.role === 'user' &&
    (metadata == null || (metadata.actorId != null && metadata.actorId === currentActorId))
  const showInsufficientCredits =
    Boolean(webConfig?.billingURL) &&
    message.parts.some(
      (part) =>
        (part.type === 'data-model-error' && isInsufficientCreditsModelError(part.data.text)) ||
        (part.type === 'dynamic-tool' && isInsufficientCreditsToolError(part.toolErrorCode)),
    )

  return (
    <Message align={mine ? 'end' : 'start'}>
      <MessageContent>
        {message.role === 'user' && !mine && metadata?.actorId != null ? (
          <ActorLabel actorID={metadata.actorId} orgID={orgID} projectID={projectID} />
        ) : null}
        {message.parts.map((part) => {
          if (part.type === 'text') {
            if (message.role === 'assistant') {
              return <AssistantMarkdown key={part.id}>{part.text}</AssistantMarkdown>
            }
            return (
              <Bubble
                key={part.id}
                align={mine ? 'end' : 'start'}
                variant={mine ? 'default' : 'secondary'}
              >
                <BubbleContent className="whitespace-pre-wrap">{part.text}</BubbleContent>
              </Bubble>
            )
          }
          if (part.type === 'reasoning') {
            return (
              <Collapsible key={part.id} className="group/reasoning max-w-[90%]">
                <CollapsibleTrigger className="text-muted-foreground hover:text-foreground flex items-center gap-1.5 text-xs">
                  <Brain className="size-3.5" /> Reasoning
                  <ChevronRight className="size-3.5 transition-transform group-data-[state=open]/reasoning:rotate-90" />
                </CollapsibleTrigger>
                <CollapsibleContent className="text-muted-foreground mt-2 border-l pl-3 text-sm whitespace-pre-wrap">
                  {part.text}
                </CollapsibleContent>
              </Collapsible>
            )
          }
          if (part.type === 'data-thinking') {
            if (!part.data.active) return null
            return (
              <div
                key={part.id}
                className="text-muted-foreground flex items-center gap-2 py-0.5 text-xs"
                role="status"
              >
                <CircleDashed className="size-3.5 motion-safe:animate-spin" />
                Thinking…
              </div>
            )
          }
          if (part.type === 'data-agent-config') {
            return <AgentConfigDivider key={part.id} action={part.data.action} />
          }
          if (part.type === 'data-model-error') {
            if (showInsufficientCredits && isInsufficientCreditsModelError(part.data.text)) {
              return null
            }
            return (
              <Bubble key={part.id} align="start" variant="destructive">
                <BubbleContent className="whitespace-pre-wrap">{part.data.text}</BubbleContent>
              </Bubble>
            )
          }
          if (part.type === 'data-media') {
            return (
              <AgentArtifactCard
                key={part.id}
                artifactID={part.data.artifactId}
                orgID={orgID}
                projectID={projectID}
                agentID={agentID}
                data={part.data.data}
                mediaType={part.data.mediaType}
                filename={part.data.filename}
                sizeBytes={part.data.sizeBytes}
              />
            )
          }
          if (part.type === 'dynamic-tool') {
            return <ToolPart key={part.id} part={part} scope={{ orgID, projectID, agentID }} />
          }
          return null
        })}
        {showInsufficientCredits && (
          <Bubble align="start" variant="destructive">
            <BubbleContent className="whitespace-pre-wrap">
              <InsufficientCreditsMessage billingHref={webConfig?.billingHref} />
            </BubbleContent>
          </Bubble>
        )}
      </MessageContent>
    </Message>
  )
}
