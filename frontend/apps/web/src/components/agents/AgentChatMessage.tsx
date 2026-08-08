import { type OmnaraUIMessage, useOmnaraClient } from '@omnara/react'
import { getActorOptions } from '@omnara/sdk/tanstack'
import { useQuery } from '@tanstack/react-query'
import { Brain, Check, ChevronRight, CircleDashed, Terminal } from 'lucide-react'
import { Streamdown } from 'streamdown'

import { AgentAttachment } from '@/components/agents/AgentAttachment'
import { Bubble, BubbleContent } from '@/components/ui/bubble'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { Message, MessageContent, MessageHeader } from '@/components/ui/message'

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
  const provider =
    actor.provider === 'slack' ? 'Slack' : actor.provider === 'external' ? 'External' : 'Omnara'
  return (
    <MessageHeader>
      {displayName == null || displayName === '' ? actor.provider_user_id : displayName}
      {` · ${provider}`}
    </MessageHeader>
  )
}

function JsonValue({ value }: { value: unknown }) {
  return (
    <pre className="bg-background/60 max-h-72 overflow-auto rounded-md border p-3 font-mono text-xs leading-relaxed">
      {JSON.stringify(value, null, 2)}
    </pre>
  )
}

function AssistantMarkdown({ children }: { children: string }) {
  return (
    <Streamdown
      className="max-w-3xl text-pretty py-0.5 text-sm leading-7"
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
      <span className="text-muted-foreground text-[11px] font-medium tracking-wide">{label}</span>
      <span className="bg-border h-px flex-1" aria-hidden="true" />
    </div>
  )
}

function ToolPart({
  part,
}: {
  part: Extract<OmnaraUIMessage['parts'][number], { type: 'dynamic-tool' }>
}) {
  const complete = part.state === 'output-available'
  return (
    <Collapsible className="group/tool bg-muted/35 overflow-hidden rounded-lg border">
      <CollapsibleTrigger className="flex w-full items-center gap-2 px-3 py-2 text-left text-xs font-medium">
        <Terminal className="text-muted-foreground size-3.5" />
        <span className="min-w-0 flex-1 truncate">{part.toolName}</span>
        {complete ? (
          <Check className="text-muted-foreground size-3.5" />
        ) : (
          <CircleDashed className="text-primary size-3.5 animate-spin" />
        )}
        <ChevronRight className="text-muted-foreground size-3.5 transition-transform group-data-[state=open]/tool:rotate-90" />
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-2 border-t p-2">
        <p className="text-muted-foreground px-1 text-[11px] font-medium">Input</p>
        <JsonValue value={part.input} />
        {part.state === 'output-available' && (
          <>
            <p className="text-muted-foreground px-1 pt-1 text-[11px] font-medium">Output</p>
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

export function AgentChatMessage({
  message,
  currentActorId,
  orgID,
  projectID,
}: {
  message: OmnaraUIMessage
  currentActorId?: string
  orgID: string
  projectID: string
}) {
  const metadata = message.metadata
  const mine =
    message.role === 'user' &&
    (metadata == null || (metadata.actorId != null && metadata.actorId === currentActorId))

  return (
    <Message align={mine ? 'end' : 'start'}>
      <MessageContent>
        {message.role === 'user' && !mine && metadata?.actorId != null ? (
          <ActorLabel actorID={metadata.actorId} orgID={orgID} projectID={projectID} />
        ) : null}
        {message.parts.map((part, index) => {
          if (part.type === 'text') {
            if (message.role === 'assistant') {
              return <AssistantMarkdown key={index}>{part.text}</AssistantMarkdown>
            }
            return (
              <Bubble
                key={index}
                align={mine ? 'end' : 'start'}
                variant={mine ? 'default' : 'secondary'}
              >
                <BubbleContent className="whitespace-pre-wrap">{part.text}</BubbleContent>
              </Bubble>
            )
          }
          if (part.type === 'reasoning') {
            return (
              <Collapsible key={index} className="group/reasoning max-w-[90%]">
                <CollapsibleTrigger className="text-muted-foreground hover:text-foreground flex items-center gap-1.5 text-xs">
                  <Brain className="size-3.5" /> Reasoning
                  <ChevronRight className="size-3.5 transition-transform group-data-[state=open]/reasoning:rotate-90" />
                </CollapsibleTrigger>
                <CollapsibleContent className="text-muted-foreground mt-2 whitespace-pre-wrap border-l pl-3 text-sm">
                  {part.text}
                </CollapsibleContent>
              </Collapsible>
            )
          }
          if (part.type === 'data-thinking') {
            if (!part.data.active) return null
            return (
              <div
                key={part.id ?? index}
                className="text-muted-foreground flex items-center gap-2 py-0.5 text-xs"
                role="status"
              >
                <CircleDashed className="size-3.5 animate-spin" />
                Thinking…
              </div>
            )
          }
          if (part.type === 'data-agent-config') {
            return <AgentConfigDivider key={part.id ?? index} action={part.data.action} />
          }
          if (part.type === 'data-media') {
            return <AgentAttachment key={part.id ?? index} artifactId={part.data.artifactId} />
          }
          if (part.type === 'dynamic-tool') return <ToolPart key={part.toolCallId} part={part} />
          return null
        })}
      </MessageContent>
    </Message>
  )
}
