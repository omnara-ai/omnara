import { useAgentUsage } from '@omnara/react'
import type { UsageReport } from '@omnara/sdk'
import { useState } from 'react'

import { DetailList } from '@/components/data-table/DetailList'
import { ChevronDown, UserGroupIcon, UserIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'
import {
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuItem,
} from '@/components/ui/sidebar'
import { Tooltip, TooltipContent, TooltipTrigger } from '@/components/ui/tooltip'
import { CostInfo } from '@/components/usage/CostInfo'
import { UsageCost } from '@/components/usage/UsageCost'
import { formatCompactCount, formatCount } from '@/lib/format'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

export function AgentUsageGroup({
  orgId,
  projectId,
  agentId,
}: {
  orgId: string
  projectId: string
  agentId: string
}) {
  // Includes subagents by default, matching the cost on the agent's card.
  const [includeSubagents, setIncludeSubagents] = useState(true)
  const query = useAgentUsage(orgId, projectId, agentId, includeSubagents)
  return (
    <SidebarGroup>
      <div className="flex items-center justify-between gap-2">
        <div className="flex items-center gap-1">
          <SidebarGroupLabel className="px-0 text-sm">Usage</SidebarGroupLabel>
          <CostInfo />
        </div>
        <SubagentsToggle
          checked={includeSubagents}
          onToggle={() => {
            setIncludeSubagents((value) => !value)
          }}
        />
      </div>
      <SidebarGroupContent>
        {query.isPending ? (
          <p className="text-muted-foreground truncate py-1.5 text-sm">Loading…</p>
        ) : query.isError ? (
          <p className="text-destructive py-1.5 text-sm">
            {errorMessage(query.error, 'Could not load usage.')}
          </p>
        ) : query.data.totals.model_calls === 0 ? (
          // All-time usage, so "yet" holds here unlike the 30-day usage pages.
          <p className="text-muted-foreground py-1.5 text-sm">
            {includeSubagents
              ? "This agent and its subagents haven't used a model yet."
              : "This agent hasn't used a model yet."}
          </p>
        ) : (
          <AgentUsageDetails report={query.data} />
        )}
      </SidebarGroupContent>
    </SidebarGroup>
  )
}

function SubagentsToggle({ checked, onToggle }: { checked: boolean; onToggle: () => void }) {
  const label = checked ? 'Exclude subagents' : 'Include subagents'
  return (
    <Tooltip>
      <TooltipTrigger asChild>
        <Button
          size="icon"
          variant="ghost"
          aria-pressed={checked}
          aria-label={label}
          className={cn('size-7', checked ? 'text-foreground' : 'text-muted-foreground')}
          onClick={onToggle}
        >
          {checked ? <UserGroupIcon className="size-4" /> : <UserIcon className="size-4" />}
        </Button>
      </TooltipTrigger>
      <TooltipContent side="left">{label}</TooltipContent>
    </Tooltip>
  )
}

function AgentUsageDetails({ report }: { report: UsageReport }) {
  const [expanded, setExpanded] = useState(false)
  return (
    <Collapsible open={expanded} onOpenChange={setExpanded}>
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="hover:text-foreground flex w-full items-center justify-between gap-2 py-1.5 text-left text-sm"
          aria-label={expanded ? 'Hide usage details' : 'Show usage details'}
        >
          <span className="truncate tabular-nums">
            <UsageCost modelCalls={report.totals.model_calls} cost={report.totals.cost} />
            <span className="text-muted-foreground">
              {' · '}
              {formatCompactCount(report.totals.tokens.input_tokens_total)} in
              {' · '}
              {formatCompactCount(report.totals.tokens.output_tokens_total)} out
            </span>
          </span>
          <ChevronDown
            className={cn(
              'text-muted-foreground size-4 shrink-0 transition-transform',
              expanded && 'rotate-180',
            )}
          />
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="flex flex-col gap-3 pb-1">
        <DetailList
          items={[
            { label: 'Calls', value: formatCount(report.totals.model_calls) },
            {
              label: 'Uncached',
              value: formatCount(report.totals.tokens.uncached_input_tokens),
            },
            {
              label: 'Cache read',
              value: formatCount(report.totals.tokens.cache_read_input_tokens),
            },
            {
              label: 'Cache write',
              value: formatCount(report.totals.tokens.cache_write_input_tokens),
            },
            {
              label: 'Reasoning',
              value: formatCount(report.totals.tokens.reasoning_output_tokens),
            },
          ]}
        />
        {report.by_model.length > 1 && (
          <SidebarMenu>
            {report.by_model.map((row) => (
              <SidebarMenuItem
                key={`${row.model.configured_model_id}:${row.model.provider_model_slug}`}
                className="flex items-center justify-between gap-2 py-1 text-xs"
              >
                <span className="truncate">{row.model.name}</span>
                <span className="text-muted-foreground shrink-0 tabular-nums">
                  {formatCompactCount(row.tokens.input_tokens_total)} in ·{' '}
                  {formatCompactCount(row.tokens.output_tokens_total)} out ·{' '}
                  <UsageCost modelCalls={row.model_calls} cost={row.cost} />
                </span>
              </SidebarMenuItem>
            ))}
          </SidebarMenu>
        )}
      </CollapsibleContent>
    </Collapsible>
  )
}
