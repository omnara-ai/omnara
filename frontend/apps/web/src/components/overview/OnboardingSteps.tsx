import { type ReactNode, useEffect, useRef, useState, ViewTransition } from 'react'

import { cn } from '@/lib/utils'

export type StepStatus = 'done' | 'active' | 'upcoming'

export function OnboardingSteps({ children }: { children: ReactNode }) {
  return (
    <div className="relative flex flex-col gap-6 pb-8 pt-2 sm:gap-12 sm:pb-12 sm:pt-4">
      {children}
    </div>
  )
}

export function OnboardingStep({
  index,
  title,
  doneTitle = title,
  description,
  status,
  nextStatus,
  pending = false,
  completion,
  children,
}: {
  index: number
  title: string
  doneTitle?: string
  description: string
  status: StepStatus
  nextStatus?: StepStatus
  pending?: boolean
  completion?: ReactNode
  children?: ReactNode
}) {
  const item = useRef<HTMLDivElement>(null)
  const [initialStatus] = useState(status)
  const previousStatus = useRef(status)
  const statusChanged = status !== initialStatus

  useEffect(() => {
    const wasUpcoming = previousStatus.current === 'upcoming'
    previousStatus.current = status
    if (!wasUpcoming || status !== 'active') return
    const timer = window.setTimeout(() => {
      item.current?.scrollIntoView({ behavior: 'smooth', block: 'center' })
    }, 550)
    return () => {
      window.clearTimeout(timer)
    }
  }, [status])

  return (
    <ViewTransition name={`onboarding-step-${index}`}>
      <div
        ref={item}
        data-step={index}
        data-status={status}
        aria-current={status === 'active' ? 'step' : undefined}
        className={cn(
          'relative flex transition-opacity duration-500 sm:pl-3',
          status === 'done' && 'sm:my-6',
          status === 'upcoming' && 'pointer-events-none select-none opacity-40',
        )}
      >
        {nextStatus && (
          <span
            aria-hidden="true"
            data-slot="step-rail"
            className={cn(
              'absolute -left-[43px] hidden w-px transition-colors duration-700 sm:block',
              'top-[20px]',
              status === 'done' && nextStatus === 'done'
                ? '-bottom-[105px]'
                : status === 'done' || nextStatus === 'done'
                  ? '-bottom-[81px]'
                  : '-bottom-[57px]',
              status === 'done' && nextStatus === 'done' && 'bg-blue-500/60',
              status === 'done' &&
                nextStatus !== 'done' &&
                'to-border bg-gradient-to-b from-blue-500',
              status !== 'done' && 'bg-border',
            )}
          />
        )}
        <span
          aria-hidden="true"
          className={cn(
            'bg-background absolute -left-12 top-[9px] hidden size-[11px] rounded-full border-2 transition-colors duration-500 sm:block',
            status === 'done' && 'border-blue-500/80',
            status === 'active' && 'border-foreground',
            status === 'upcoming' && 'border-muted-foreground',
          )}
        />
        <div
          className={cn(
            'min-w-0 flex-1 rounded-2xl p-px sm:-m-6',
            status === 'done' && 'bg-gradient-to-r from-blue-500/25 to-transparent',
            status === 'done' && statusChanged && 'animate-in fade-in-0 duration-500',
          )}
        >
          <div
            className={cn(
              'min-w-0 rounded-[15px] p-[15px] sm:p-[23px]',
              status === 'done' &&
                'bg-background bg-gradient-to-r from-blue-500/[0.07] to-transparent',
            )}
          >
            <div className="flex min-w-0 flex-col gap-5 sm:gap-8">
              <div className="flex flex-col items-start gap-1.5 sm:flex-row sm:flex-wrap sm:items-center sm:justify-between sm:gap-x-6">
                <div className="flex flex-col gap-1.5">
                  <h3 className="flex items-center gap-2 text-lg font-semibold tracking-tight sm:text-xl">
                    {status === 'done' ? doneTitle : title}
                    {pending && status !== 'done' && (
                      <span
                        role="status"
                        aria-label="Working"
                        data-slot="step-spinner"
                        className="border-muted-foreground/25 size-4 animate-spin rounded-full border-2 border-t-blue-500"
                      />
                    )}
                  </h3>
                  {status !== 'done' && (
                    <p className="text-muted-foreground text-sm">{description}</p>
                  )}
                </div>
                {status === 'done' && completion && (
                  <div
                    data-slot="step-completion"
                    className="flex items-center text-sm font-medium text-blue-600/70 dark:text-blue-300/70"
                  >
                    {completion}
                  </div>
                )}
              </div>
              {children}
            </div>
          </div>
        </div>
      </div>
    </ViewTransition>
  )
}
