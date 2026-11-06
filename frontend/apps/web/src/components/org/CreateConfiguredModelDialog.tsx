import type { ModelProviderConfig } from '@omnara/sdk'

import { Dialog, DialogContent } from '@/components/ui/dialog'

import { AddConfiguredModelsView } from './AddConfiguredModelsView'

export function CreateConfiguredModelDialog({
  open,
  onOpenChange,
  orgId,
  providers,
  defaultProviderId,
  defaultProjectIds,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  providers: ModelProviderConfig[]
  defaultProviderId?: string
  defaultProjectIds?: string[]
}) {
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-2xl">
        <AddConfiguredModelsView
          orgId={orgId}
          providers={providers}
          defaultProviderId={defaultProviderId}
          defaultProjectIds={defaultProjectIds}
          onDone={() => {
            onOpenChange(false)
          }}
        />
      </DialogContent>
    </Dialog>
  )
}
