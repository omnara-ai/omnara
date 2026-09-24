import type { IntegrationType } from '@omnara/sdk'

import { cn } from '@/lib/utils'

import { integrationCatalog } from './integrationDefinitions'

export function IntegrationIcon({
  integrationType,
  className,
}: {
  integrationType: IntegrationType
  className?: string
}) {
  const integration = integrationCatalog.find(
    (integration) => integration.integrationType === integrationType,
  )
  if (!integration) return null
  const darkLogo = 'darkLogo' in integration ? integration.darkLogo : undefined
  return (
    <span className={cn('inline-flex size-6 shrink-0', className)} aria-hidden="true">
      <img
        src={integration.logo}
        alt=""
        className={cn('size-full object-contain', darkLogo && 'dark:hidden')}
      />
      {darkLogo && (
        <img src={darkLogo} alt="" className="hidden size-full object-contain dark:block" />
      )}
    </span>
  )
}
