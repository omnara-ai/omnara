import { useDeleteOtelExporter, useOtelExporters } from '@omnara/react'
import type { OtelExporter } from '@omnara/sdk'
import { useState } from 'react'

import {
  AgentCard,
  AgentCardGlyph,
  AgentCardStat,
  AgentCardTime,
} from '@/components/agents/AgentCardList'
import { Bot, Folder, FunnelIcon, KeyRound, PlusIcon } from '@/components/icons'
import { OtelExporterDialog } from '@/components/otel-exporters/OtelExporterDialog'
import {
  otelAuthKindLabels,
  otelEncodingLabels,
  otelVariantLabels,
} from '@/components/otel-exporters/otelExporterOptions'
import { OtelExporterScopeLabel } from '@/components/otel-exporters/OtelExporterScopeField'
import { OtelVariantLogo } from '@/components/otel-exporters/OtelVariantLogo'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { Button } from '@/components/ui/button'
import { Empty, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Spinner } from '@/components/ui/spinner'
import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'
import { errorMessage } from '@/lib/submit-status'

const scopeIcons = { project: Folder, agent: Bot }

/**
 * The organization's exporters fetched in a single list. Inside a project the list narrows
 * to the project's own and its agents' exporters.
 */
export function OtelExportersSection({ orgId, projectId }: { orgId: string; projectId?: string }) {
  const query = useOtelExporters(orgId)
  const exporters = useInfiniteQueryItems(query).filter(
    (exporter) => projectId === undefined || exporter.scope.project_id === projectId,
  )
  const remove = useDeleteOtelExporter(orgId)
  const [editing, setEditing] = useState<OtelExporter | 'new' | null>(null)
  return (
    <section className="flex min-w-0 flex-col gap-3">
      <Button
        size="sm"
        variant="outline"
        className="self-end"
        onClick={() => {
          setEditing('new')
        }}
      >
        <PlusIcon /> Add exporter
      </Button>
      {exporters.length > 0 && (
        <ul className="flex flex-col gap-2">
          {exporters.map((exporter) => {
            const ScopeIcon = scopeIcons[exporter.scope.type]
            const extraHeaders =
              Object.keys(exporter.headers).length + Object.keys(exporter.secret_headers).length
            return (
              <li key={exporter.id}>
                <AgentCard
                  icon={
                    <AgentCardGlyph>
                      <OtelVariantLogo variant={exporter.variant} />
                    </AgentCardGlyph>
                  }
                  title={
                    <span className="truncate font-medium" title={exporter.endpoint}>
                      {exporter.endpoint}
                    </span>
                  }
                  subtitle={
                    <>
                      <span className="shrink-0">{otelVariantLabels[exporter.variant]}</span>
                      <span aria-hidden="true">·</span>
                      <span className="shrink-0">{otelEncodingLabels[exporter.encoding]}</span>
                      {exporter.auth_kind !== 'none' && (
                        <>
                          <span aria-hidden="true">·</span>
                          <span className="shrink-0">{otelAuthKindLabels[exporter.auth_kind]}</span>
                        </>
                      )}
                    </>
                  }
                  meta={
                    <>
                      <AgentCardTime label="Updated" value={exporter.updated_at} />
                      <ResourceRowActions
                        onEdit={() => {
                          setEditing(exporter)
                        }}
                        onDelete={() => {
                          if (window.confirm('Delete exporter ' + exporter.endpoint + '?'))
                            remove.mutate(exporter.id)
                        }}
                      />
                    </>
                  }
                  footer={
                    <span className="flex min-w-0 items-center gap-1.5">
                      <ScopeIcon className="size-4 shrink-0" aria-hidden="true" />
                      <OtelExporterScopeLabel
                        orgId={orgId}
                        projectId={projectId}
                        scope={exporter.scope}
                      />
                    </span>
                  }
                  stats={
                    <>
                      <AgentCardStat
                        icon={FunnelIcon}
                        label="of turns"
                        value={`${String(exporter.sample_percent)}%`}
                      />
                      <AgentCardStat
                        icon={KeyRound}
                        label={extraHeaders === 1 ? 'header' : 'headers'}
                        value={extraHeaders}
                      />
                    </>
                  }
                />
              </li>
            )
          })}
        </ul>
      )}
      {query.isPending && <Spinner className="text-muted-foreground size-4" />}
      {query.isError && (
        <div className="flex items-center gap-2">
          <p role="alert" className="text-destructive text-sm">
            Could not load all exporters.
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
      {!query.isPending && !query.isError && !query.hasNextPage && exporters.length === 0 && (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>No exporters.</EmptyDescription>
          </EmptyHeader>
        </Empty>
      )}
      {query.hasNextPage && (
        <Button
          variant="outline"
          className="self-start"
          loading={query.isFetchingNextPage}
          onClick={() => {
            void query.fetchNextPage()
          }}
        >
          Load more exporters
        </Button>
      )}
      {remove.isError && (
        <p role="alert" className="text-destructive text-sm">
          {errorMessage(remove.error, 'Could not delete exporter')}
        </p>
      )}
      {editing && (
        <OtelExporterDialog
          orgId={orgId}
          projectId={projectId}
          exporter={editing === 'new' ? undefined : editing}
          onClose={() => {
            setEditing(null)
          }}
        />
      )}
    </section>
  )
}
