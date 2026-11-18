import { useAgentTurns, useTurnTrace } from '@omnara/react'
import { type ReactNode, useState } from 'react'
import { createPortal } from 'react-dom'

import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { Spinner } from '@/components/ui/spinner'

import { parseTrace } from './otlpTrace'
import { SpanDetails } from './SpanDetails'
import { TraceWaterfall } from './TraceWaterfall'

interface Scope {
  orgID: string
  projectID: string
  agentID: string
}

/** The chosen turn's trace: its spans above the selected span's details. */
export function AgentTraces({
  orgID,
  projectID,
  agentID,
  toolbar,
}: Scope & {
  /** Where the turn picker goes: the page header, beside the view tabs. */
  toolbar: HTMLElement | null
}) {
  const scope = { orgID, projectID, agentID }
  const turnsQuery = useAgentTurns(scope)
  const turns = turnsQuery.data?.turns ?? []
  const [chosenTurnId, setChosenTurnId] = useState<string>()
  // Until a turn is picked, the latest one is shown.
  const turn = turns.find((item) => item.id === chosenTurnId) ?? turns.at(0)

  return (
    <div className="flex h-full min-h-0 w-full flex-col gap-3">
      {turnsQuery.isPending && <Spinner className="text-muted-foreground size-4" />}
      {turnsQuery.isError && (
        <div className="flex items-center gap-2">
          <p role="alert" className="text-destructive text-sm">
            Could not load turns.
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void turnsQuery.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      )}
      {turnsQuery.isSuccess && !turn && (
        <p className="text-muted-foreground text-sm">This agent has no turns yet.</p>
      )}
      {turn && (
        <TurnTrace
          key={turn.id}
          scope={scope}
          turnId={turn.id}
          toolbar={toolbar}
          picker={
            <>
              <Select value={turn.id} onValueChange={setChosenTurnId}>
                <SelectTrigger aria-label="Turn" size="sm" className="w-auto shrink-0">
                  <SelectValue>Turn {turn.turn_sequence}</SelectValue>
                </SelectTrigger>
                <SelectContent>
                  {turns.map((item) => (
                    <SelectItem key={item.id} value={item.id}>
                      Turn {item.turn_sequence}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
              {turnsQuery.hasNextPage && (
                <Button
                  size="sm"
                  variant="outline"
                  className="shrink-0"
                  loading={turnsQuery.isFetchingNextPage}
                  onClick={() => {
                    void turnsQuery.fetchNextPage()
                  }}
                >
                  Load older turns
                </Button>
              )}
            </>
          }
        />
      )}
    </div>
  )
}

function TurnTrace({
  scope,
  turnId,
  toolbar,
  picker,
}: {
  scope: Scope
  turnId: string
  toolbar: HTMLElement | null
  /** The turn selector, which heads the trace in every state. */
  picker: ReactNode
}) {
  const query = useTurnTrace(scope, turnId)
  const trace = query.data ? parseTrace(query.data) : undefined
  const [chosenSpanId, setChosenSpanId] = useState<string>()
  // Until a span is picked, the turn's root span is shown.
  const selected = trace?.spans.find((span) => span.id === chosenSpanId) ?? trace?.spans.at(0)
  const truncated = trace?.spans.some((span) =>
    span.attributes.some(({ key }) => key === 'omnara.trace.truncated'),
  )

  return (
    <>
      {toolbar &&
        createPortal(
          <>
            {picker}
            {query.isPending && <Spinner className="text-muted-foreground size-4" />}
            {trace && trace.spans.length > 0 && (
              <p className="text-muted-foreground truncate text-sm">
                {trace.spans.length === 1 ? '1 span' : `${String(trace.spans.length)} spans`}
                {truncated && ' · Earliest events omitted'}
              </p>
            )}
          </>,
          toolbar,
        )}
      {!query.isPending && !trace && (
        <div className="flex items-center gap-2">
          <p role="alert" className="text-destructive text-sm">
            Could not load this turn&rsquo;s trace.
          </p>
          <Button
            size="sm"
            variant="outline"
            onClick={() => {
              void query.refetch()
            }}
          >
            Retry
          </Button>
        </div>
      )}
      {trace?.spans.length === 0 && (
        <p className="text-muted-foreground text-sm">Nothing in this turn has finished yet.</p>
      )}
      {trace && selected && (
        <>
          {/* The span list stays put; the selected span's card tucks in beneath it and scrolls. */}
          <div className="bg-background relative z-10 max-h-[40%] shrink-0 overflow-y-auto rounded-xl border p-1 shadow-sm">
            <TraceWaterfall trace={trace} selectedId={selected.id} onSelect={setChosenSpanId} />
          </div>
          <div className="bg-muted/40 -mt-7 min-h-0 flex-1 overflow-y-auto rounded-xl border px-4 pt-8 pb-4">
            <SpanDetails orgId={scope.orgID} projectId={scope.projectID} span={selected} />
          </div>
        </>
      )}
    </>
  )
}
