import { Popover } from 'radix-ui'
import { useContext } from 'react'

import { InfoIcon } from '@/components/icons'
import { DialogContainerContext } from '@/components/ui/dialog-container-context'
import { docsUrl, guides } from '@/lib/docs'
import { useWebConfig } from '@/lib/web-config'

export function CostInfo() {
  const { data: webConfig } = useWebConfig()
  const dialogContainer = useContext(DialogContainerContext)
  return (
    <Popover.Root>
      <Popover.Trigger asChild>
        <button
          type="button"
          aria-label="About costs"
          className="text-muted-foreground hover:text-foreground inline-flex shrink-0 transition-colors"
        >
          <InfoIcon className="size-3.5" aria-hidden="true" />
        </button>
      </Popover.Trigger>
      <Popover.Portal container={dialogContainer ?? undefined}>
        <Popover.Content
          aria-label="About costs"
          side="bottom"
          sideOffset={6}
          className="text-popover-foreground bg-popover type-caption ring-border animate-in fade-in-0 zoom-in-95 data-[state=closed]:animate-out data-[state=closed]:fade-out-0 data-[state=closed]:zoom-out-95 z-50 w-fit max-w-xs origin-(--radix-popover-content-transform-origin) rounded-md px-3 py-1.5 text-balance shadow-md ring-1 outline-none"
        >
          Costs may include estimates.{' '}
          <a
            className="font-medium underline underline-offset-2"
            href={docsUrl(guides.costs)}
            target="_blank"
            rel="noreferrer"
          >
            How costs are calculated
          </a>
          {webConfig?.billingHref && (
            <>
              {' '}
              See what Omnara charged on the{' '}
              <a className="font-medium underline underline-offset-2" href={webConfig.billingHref}>
                Credits page
              </a>
              .
            </>
          )}
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  )
}
