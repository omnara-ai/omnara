import { CatchBoundary } from '@tanstack/react-router'
import { type ComponentProps, lazy, Suspense } from 'react'

import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'

const TextFileEditor = lazy(async () => {
  const module = await import('@/components/ui/text-file-editor')
  return { default: module.TextFileEditor }
})

function SkillMdEditorFallback({ className }: { className?: string }) {
  return (
    <div
      className={cn(
        'border-input bg-card type-code rounded-control flex h-80 items-center justify-center overflow-hidden border',
        className,
      )}
      role="status"
      aria-live="polite"
    >
      <Spinner className="text-muted-foreground size-6" />
      <span className="sr-only">Loading SKILL.md editor</span>
    </div>
  )
}

function SkillMdEditorError() {
  return <p className="text-destructive text-sm">Could not load the SKILL.md editor.</p>
}

export function LazySkillMdEditor(props: Omit<ComponentProps<typeof TextFileEditor>, 'filename'>) {
  return (
    <CatchBoundary getResetKey={() => props.id} errorComponent={SkillMdEditorError}>
      <Suspense fallback={<SkillMdEditorFallback className={props.className} />}>
        <TextFileEditor filename="SKILL.md" {...props} />
      </Suspense>
    </CatchBoundary>
  )
}
