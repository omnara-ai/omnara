import type { OmnaraUIMessage } from '@omnara/react'
import * as z from 'zod'

import {
  type ActivityItem,
  describeReasoning,
  describeTool,
  summarizeActivity,
  toolInputSchema,
  type ToolStatus,
} from '@/components/agents/toolLabels'
import { ChevronRight, CircleDashed } from '@/components/icons'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import { cn } from '@/lib/utils'

export type ToolPart = Extract<OmnaraUIMessage['parts'][number], { type: 'dynamic-tool' }>
export type ReasoningPart = Extract<OmnaraUIMessage['parts'][number], { type: 'reasoning' }>

// A finished call's output as the chat stream builds it: the outcome plus the result blocks.
const toolOutputSchema = z
  .object({
    outcome: z.string().optional(),
    contentBlocks: z.array(z.object({ text: z.string().optional() })).optional(),
  })
  .catch({})

function toolStatus(part: ToolPart): ToolStatus {
  if (part.state === 'output-error') return 'failed'
  if (part.state !== 'output-available') return 'running'
  return toolOutputSchema.parse(part.output).outcome === 'failed' ? 'failed' : 'done'
}

function activityItem(part: ToolPart | ReasoningPart): ActivityItem {
  if (part.type === 'reasoning') {
    return { kind: 'reasoning', status: part.state === 'streaming' ? 'running' : 'done' }
  }
  return {
    kind: 'tool',
    name: part.toolName,
    input: toolInputSchema.parse(part.input),
    status: toolStatus(part),
  }
}

/**
 * A run of consecutive tool calls, and the reasoning between them, as one plain-English line,
 * e.g. "Reasoned, ran 3 commands". It expands into a row per step; each row expands again to
 * show the reasoning, or what a call was given and what it returned.
 */
export function AgentToolGroup({ parts }: { parts: (ToolPart | ReasoningPart)[] }) {
  const steps = parts.map((part) => ({ part, item: activityItem(part) }))
  const running = steps.some((step) => step.item.status === 'running')
  const [only] = steps

  return (
    <Collapsible className="group/tools my-1.5 w-full max-w-3xl">
      <CollapsibleTrigger className="text-muted-foreground hover:text-foreground flex max-w-full items-center gap-1.5 text-left text-sm transition-colors">
        {running && <CircleDashed className="size-3.5 shrink-0 motion-safe:animate-spin" />}
        <span className="min-w-0 truncate">
          {summarizeActivity(steps.map((step) => step.item))}
        </span>
        <ChevronRight
          aria-hidden="true"
          className="size-3.5 shrink-0 transition-transform group-data-[state=open]/tools:rotate-90"
        />
      </CollapsibleTrigger>
      <CollapsibleContent>
        {steps.length === 1 && only?.part.type === 'dynamic-tool' ? (
          <ToolDetails part={only.part} className="mt-2" />
        ) : steps.length === 1 && only?.part.type === 'reasoning' ? (
          <p className="text-muted-foreground mt-2 text-sm whitespace-pre-wrap">{only.part.text}</p>
        ) : (
          <div className="mt-2 divide-y overflow-hidden rounded-lg border">
            {steps.map(({ part, item }) =>
              part.type === 'reasoning' ? (
                <ReasoningRow key={part.id} part={part} status={item.status} />
              ) : (
                <ToolRow key={part.id} part={part} status={item.status} />
              ),
            )}
          </div>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}

function ToolRow({ part, status }: { part: ToolPart; status: ToolStatus }) {
  const { verb, target } = describeTool(part.toolName, toolInputSchema.parse(part.input), status)
  return (
    <Collapsible className="group/tool">
      <CollapsibleTrigger className="hover:bg-muted/50 flex w-full items-center gap-1.5 px-3 py-2 text-left text-sm transition-colors">
        {status === 'running' && (
          <CircleDashed className="text-muted-foreground size-3.5 shrink-0 motion-safe:animate-spin" />
        )}
        <span
          className={cn(
            'min-w-0 truncate',
            status === 'failed' ? 'text-destructive' : 'text-muted-foreground',
          )}
        >
          {verb} <span className="text-foreground font-medium">{target}</span>
        </span>
        <ChevronRight
          aria-hidden="true"
          className="text-muted-foreground size-3.5 shrink-0 transition-transform group-data-[state=open]/tool:rotate-90"
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="px-3 pb-3">
        <ToolDetails part={part} />
      </CollapsibleContent>
    </Collapsible>
  )
}

function ReasoningRow({ part, status }: { part: ReasoningPart; status: ToolStatus }) {
  return (
    <Collapsible className="group/tool">
      <CollapsibleTrigger className="text-muted-foreground hover:bg-muted/50 flex w-full items-center gap-1.5 px-3 py-2 text-left text-sm transition-colors">
        {status === 'running' && (
          <CircleDashed className="size-3.5 shrink-0 motion-safe:animate-spin" />
        )}
        <span className="min-w-0 truncate">{describeReasoning(status)}</span>
        <ChevronRight
          aria-hidden="true"
          className="size-3.5 shrink-0 transition-transform group-data-[state=open]/tool:rotate-90"
        />
      </CollapsibleTrigger>
      <CollapsibleContent className="text-muted-foreground px-3 pb-3 text-sm whitespace-pre-wrap">
        {part.text}
      </CollapsibleContent>
    </Collapsible>
  )
}

/** What the call was given and what it returned, with text results shown as text. */
function ToolDetails({ part, className }: { part: ToolPart; className?: string }) {
  const result = part.state === 'output-available' ? resultText(part) : undefined
  return (
    <div className={cn('space-y-2', className)}>
      <pre className="bg-muted/40 type-code max-h-72 overflow-auto rounded-md border p-3">
        {JSON.stringify(part.input ?? {}, null, 2)}
      </pre>
      {result !== undefined && (
        <pre className="text-muted-foreground type-code max-h-72 overflow-auto whitespace-pre-wrap">
          {result}
        </pre>
      )}
      {part.state === 'output-error' && (
        <p className="text-destructive text-xs">{part.errorText}</p>
      )}
    </div>
  )
}

function resultText(part: ToolPart): string {
  const blocks = toolOutputSchema.parse(part.output).contentBlocks
  const texts = blocks?.map((block) => block.text) ?? []
  return blocks && texts.every((text) => text !== undefined)
    ? texts.join('\n')
    : JSON.stringify(part.output, null, 2)
}
