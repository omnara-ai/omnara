import { type OmnaraUIMessage, useOmnaraClient } from '@omnara/react'
import { getActorOptions } from '@omnara/sdk/tanstack'
import { useQuery } from '@tanstack/react-query'
import { Streamdown } from 'streamdown'

import { AgentArtifactCard } from '@/components/agents/AgentArtifactCard'
import {
  AgentToolGroup,
  type ReasoningPart,
  type ToolPart,
} from '@/components/agents/AgentToolGroup'
import { InsufficientCreditsMessage } from '@/components/agents/InsufficientCreditsMessage'
import { CircleDashed } from '@/components/icons'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
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

type MessagePart = OmnaraUIMessage['parts'][number]

type Step = ToolPart | ReasoningPart

function rendersNothing(part: MessagePart) {
  if (part.type === 'step-start') return true
  if (part.type === 'data-thinking') return !part.data.active
  return part.type === 'text' && part.text.trim() === ''
}

/**
 * Consecutive tool calls and reasoning become one group, so a burst of work reads as a single
 * line, whether or not it includes a tool call.
 */
function groupToolRuns(parts: MessagePart[]): (MessagePart | Step[])[] {
  const runs: (MessagePart | Step[])[] = []
  for (const part of parts) {
    // Step boundaries and other parts that draw nothing must not split a run.
    if (rendersNothing(part)) continue
    if (part.type !== 'dynamic-tool' && part.type !== 'reasoning') {
      runs.push(part)
      continue
    }
    const last = runs.at(-1)
    if (Array.isArray(last)) last.push(part)
    else runs.push([part])
  }
  return runs
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
        {groupToolRuns(message.parts).map((part) => {
          if (Array.isArray(part)) {
            return <AgentToolGroup key={part[0]?.id} parts={part} />
          }
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
