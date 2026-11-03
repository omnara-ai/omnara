import type { ReactNode } from 'react'

import { useAuroraSpecular } from '@/components/aurora/auroraStore'
import { cn } from '@/lib/utils'

/** A translucent card whose border lights up from the nearest aurora backdrop. */
export function AuroraPanel({ className, children }: { className?: string; children: ReactNode }) {
  const ringRef = useAuroraSpecular()
  return (
    <div
      className={cn(
        'group bg-card/50 relative rounded-2xl shadow-[0_10px_32px_-20px_rgba(0,0,0,0.14)] backdrop-blur-[3px]',
        className,
      )}
    >
      <div
        ref={ringRef}
        aria-hidden="true"
        className="aurora-ring pointer-events-none absolute inset-0 rounded-2xl"
      />
      {children}
    </div>
  )
}
