import type { AppType } from '@omnara/sdk'

import { cn } from '@/lib/utils'

import { appCatalog } from './appDefinitions'

export function AppIcon({ appType, className }: { appType: AppType; className?: string }) {
  const app = appCatalog.find((app) => app.appType === appType)
  if (!app) return null
  const darkLogo = 'darkLogo' in app ? app.darkLogo : undefined
  return (
    <span className={cn('inline-flex size-6 shrink-0', className)} aria-hidden="true">
      <img
        src={app.logo}
        alt=""
        className={cn('size-full object-contain', darkLogo && 'dark:hidden')}
      />
      {darkLogo && (
        <img src={darkLogo} alt="" className="hidden size-full object-contain dark:block" />
      )}
    </span>
  )
}
