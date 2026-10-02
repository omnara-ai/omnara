import { useClusterModelPricing, useModelProviders } from '@omnara/react'
import type { ModelProviderConfig } from '@omnara/sdk'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { useState } from 'react'

import { AgentCard, AgentCardStat, AgentCardTime } from '@/components/agents/AgentCardList'
import { OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { Box } from '@/components/icons'
import { PageBreadcrumb } from '@/components/layout/PageBreadcrumb'
import { SectionTitle } from '@/components/layout/SectionTitle'
import {
  type ModelDialog,
  ModelDialogs,
  ProviderActions,
} from '@/components/overview/ModelManagement'
import { ProviderGlyph, ProviderSubtitle } from '@/components/overview/ModelProvidersSection'
import { ProviderModelList } from '@/components/overview/ProviderModelList'
import { Button } from '@/components/ui/button'
import { Empty, EmptyContent, EmptyDescription, EmptyHeader } from '@/components/ui/empty'
import { Skeleton } from '@/components/ui/skeleton'
import { useAllPages } from '@/hooks/use-all-pages'
import { useModelActions } from '@/hooks/use-model-actions'
import { useProviderModels } from '@/hooks/use-provider-models'
import { formatCount } from '@/lib/format'
import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

export function ModelProviderPage() {
  const { activeOrg } = useActiveOrg()
  const { providerId = '' } = useParams({ strict: false })
  // There's no single-provider endpoint, so find it in the (short) provider list.
  const providers = useAllPages(useModelProviders(activeOrg.id))
  const provider = providers.items.find((candidate) => candidate.id === providerId)

  return (
    <div className="mx-auto flex w-full max-w-5xl flex-col gap-8">
      <PageBreadcrumb
        items={[
          { id: 'models', label: 'Models', to: '/models' },
          { id: 'provider', label: provider?.name ?? 'Provider' },
        ]}
      />
      {provider ? (
        <ProviderView provider={provider} providers={providers.items} />
      ) : providers.isPending ? (
        <Skeleton className="h-[7.25rem] rounded-xl" />
      ) : (
        <Empty className="rounded-xl border">
          <EmptyHeader>
            <EmptyDescription>
              {providers.isError
                ? 'Couldn’t load this provider.'
                : 'This provider doesn’t exist or was deleted.'}
            </EmptyDescription>
          </EmptyHeader>
          <EmptyContent>
            <Button asChild size="sm" variant="ghost">
              <Link to="/models">Back to models</Link>
            </Button>
          </EmptyContent>
        </Empty>
      )}
    </div>
  )
}

function ProviderView({
  provider,
  providers,
}: {
  provider: ModelProviderConfig
  providers: ModelProviderConfig[]
}) {
  const { activeOrg } = useActiveOrg()
  const canManage = canManageOrg(activeOrg.role)
  const navigate = useNavigate()
  const pricing = useClusterModelPricing(activeOrg.id)
  const { models, isPending, isError, refetch } = useProviderModels(activeOrg.id, provider.id)
  const [dialog, setDialog] = useState<ModelDialog>(null)
  const modelActions = useModelActions(activeOrg.id, canManage, setDialog)

  return (
    <>
      <AgentCard
        icon={<ProviderGlyph provider={provider} />}
        title={
          <>
            <h1 className="truncate font-medium">{provider.name}</h1>
            {provider.management_kind === 'cluster' && <OmnaraManagedTag />}
          </>
        }
        subtitle={<ProviderSubtitle provider={provider} />}
        meta={
          <>
            <AgentCardTime label="Updated" value={provider.updated_at} />
            {canManage && (
              <ProviderActions
                orgId={activeOrg.id}
                provider={provider}
                onEdit={() => {
                  setDialog({ kind: 'edit-provider', provider })
                }}
                onDeleted={() => {
                  void navigate({ to: '/models' })
                }}
              />
            )}
          </>
        }
        stats={
          <AgentCardStat
            icon={Box}
            label={models.length === 1 ? 'model' : 'models'}
            value={isPending || isError ? undefined : formatCount(models.length)}
          />
        }
      />
      <section className="flex flex-col gap-3">
        <SectionTitle title="Models" />
        <div className="rounded-xl border [&>div]:border-t-0">
          <ProviderModelList
            provider={provider}
            models={models}
            isPending={isPending}
            isError={isError}
            onRetry={refetch}
            pricing={pricing}
            actions={modelActions}
          />
        </div>
      </section>
      {canManage && (
        <ModelDialogs
          orgId={activeOrg.id}
          providers={providers}
          dialog={dialog}
          onClose={() => {
            setDialog(null)
          }}
        />
      )}
    </>
  )
}
