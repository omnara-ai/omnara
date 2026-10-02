import type { ConfiguredModel, ProjectModelGrantListItem } from '@omnara/sdk'
import type { DiscoveredModelPricing } from '@omnara/sdk'
import { useId, useState } from 'react'

import { OmnaraManagedTag } from '@/components/brand/OmnaraManaged'
import { ChevronDown, Plus } from '@/components/icons'
import { ModelPricingSummary } from '@/components/models/ModelPricing'
import { ResourceRowActions } from '@/components/overview/ResourceRowActions'
import { modelGrantOverrides } from '@/components/projects/grant-override-diffs'
import { OverrideChip, OverrideList } from '@/components/projects/GrantOverrides'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'

export function SharedModelRow({
  item,
  orgModel,
  pricing,
  profileCount,
  onEdit,
  onStopSharing,
}: {
  item: ProjectModelGrantListItem
  orgModel: ConfiguredModel | undefined
  pricing: DiscoveredModelPricing | undefined
  profileCount: number
  onEdit?: () => void
  onStopSharing?: () => void
}) {
  const [open, setOpen] = useState(false)
  const detailsId = useId()
  const overrides = modelGrantOverrides(item.grant, orgModel)
  // Only grants that change something have details worth opening.
  const expandable = overrides.length > 0
  const summary = (
    <>
      <span className="truncate font-medium">{item.model.name}</span>
      {orgModel?.management_kind === 'cluster' && <OmnaraManagedTag />}
      <OverrideChip count={overrides.length} />
      <span className="text-muted-foreground hidden truncate font-mono text-xs sm:inline">
        {item.model.provider_model_slug}
      </span>
      <span className="text-muted-foreground ml-auto hidden shrink-0 text-xs tabular-nums md:inline">
        {profileCount === 0
          ? 'Unused'
          : `${profileCount} ${profileCount === 1 ? 'profile' : 'profiles'}`}
      </span>
      <ModelPricingSummary
        className="text-muted-foreground shrink-0 whitespace-nowrap text-xs tabular-nums max-md:ml-auto"
        pricing={pricing}
      />
    </>
  )

  return (
    <li className="flex flex-col">
      <div className="hover:bg-accent flex min-w-0 items-center gap-2 rounded-md pr-1 transition-colors">
        {expandable ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={detailsId}
            onClick={() => {
              setOpen((value) => !value)
            }}
            className="flex min-h-9 min-w-0 flex-1 items-center gap-2.5 px-2 py-1.5 text-left text-sm"
          >
            <ChevronDown
              className={cn(
                'text-muted-foreground size-3.5 shrink-0 transition-transform duration-200 motion-reduce:transition-none',
                !open && '-rotate-90',
              )}
              aria-hidden="true"
            />
            {summary}
          </button>
        ) : (
          <div className="flex min-h-9 min-w-0 flex-1 items-center gap-2.5 py-1.5 pl-[2.125rem] pr-2 text-sm">
            {summary}
          </div>
        )}
        <ResourceRowActions deleteLabel="Stop sharing" onEdit={onEdit} onDelete={onStopSharing} />
      </div>
      {expandable && open && (
        <div id={detailsId} className="py-2 pl-8 pr-2">
          <OverrideList overrides={overrides} />
        </div>
      )}
    </li>
  )
}

export function AvailableModelRow({
  model,
  pricing,
  sharing,
  onShare,
}: {
  model: ConfiguredModel
  pricing: DiscoveredModelPricing | undefined
  sharing: boolean
  onShare: () => void
}) {
  return (
    <li className="flex min-w-0 items-center gap-2.5 rounded-md py-1 pl-[2.125rem] pr-1 text-sm">
      <span className="text-muted-foreground truncate">{model.name}</span>
      {model.management_kind === 'cluster' && <OmnaraManagedTag />}
      <span className="text-muted-foreground/70 hidden truncate font-mono text-xs sm:inline">
        {model.provider_model_slug}
      </span>
      <ModelPricingSummary
        className="text-muted-foreground/70 ml-auto shrink-0 whitespace-nowrap text-xs tabular-nums"
        pricing={pricing}
      />
      <Button
        type="button"
        size="sm"
        variant="ghost"
        className="text-primary hover:text-primary h-9 shrink-0 px-2 sm:h-7"
        loading={sharing}
        onClick={onShare}
      >
        <Plus aria-hidden="true" />
        Share
      </Button>
    </li>
  )
}
