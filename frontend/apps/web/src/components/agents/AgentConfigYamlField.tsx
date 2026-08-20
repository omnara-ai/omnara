import { CatchBoundary } from '@tanstack/react-router'
import { lazy, Suspense } from 'react'

import { Button } from '@/components/ui/button'
import { Field, RequiredFieldLabel } from '@/components/ui/field'
import { Spinner } from '@/components/ui/spinner'
import { cn } from '@/lib/utils'

import type { AgentConfigYamlEditorProps } from './AgentConfigYamlEditor'

const LazyAgentConfigYamlEditor = lazy(async () => {
  const module = await import('./AgentConfigYamlEditor')
  return { default: module.AgentConfigYamlEditor }
})

export interface AgentConfigYamlFieldProps extends AgentConfigYamlEditorProps {
  hideLabel?: boolean
}

function AgentConfigYamlEditorFallback({
  id,
  className,
}: Pick<AgentConfigYamlEditorProps, 'id' | 'className'>) {
  return (
    <div
      id={id}
      className={cn(
        'border-input bg-background flex h-[28rem] items-center justify-center overflow-hidden rounded-md border text-xs',
        className,
      )}
      role="status"
      aria-live="polite"
    >
      <Spinner className="text-muted-foreground h-6 w-6" />
      <span className="sr-only">Loading YAML editor</span>
    </div>
  )
}

function AgentConfigYamlEditorError() {
  return (
    <div
      role="alert"
      className="border-destructive/30 bg-destructive/5 flex items-center justify-between gap-3 rounded-md border px-3 py-2"
    >
      <p className="text-destructive text-sm">Could not load the YAML editor.</p>
      <Button
        type="button"
        size="sm"
        variant="outline"
        className="shrink-0"
        onClick={() => {
          window.location.reload()
        }}
      >
        Reload page
      </Button>
    </div>
  )
}

export function AgentConfigYamlField({
  hideLabel = false,
  id,
  className,
  ...editorProps
}: AgentConfigYamlFieldProps) {
  const editor = (
    <CatchBoundary getResetKey={() => id} errorComponent={AgentConfigYamlEditorError}>
      <Suspense fallback={<AgentConfigYamlEditorFallback id={id} className={className} />}>
        <LazyAgentConfigYamlEditor id={id} className={className} {...editorProps} />
      </Suspense>
    </CatchBoundary>
  )

  if (hideLabel) return editor

  return (
    <Field className="min-h-0 flex-1">
      <RequiredFieldLabel asChild>
        <span>Config (YAML)</span>
      </RequiredFieldLabel>
      {editor}
    </Field>
  )
}
