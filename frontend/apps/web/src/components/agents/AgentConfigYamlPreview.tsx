import { FileCode2Icon } from 'lucide-react'
import { useSyncExternalStore } from 'react'

import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'

import { AgentConfigYamlField } from './AgentConfigYamlField'

const largeScreenMediaQuery = '(min-width: 64rem)'

function subscribe(onStoreChange: () => void) {
  const query = window.matchMedia(largeScreenMediaQuery)
  query.addEventListener('change', onStoreChange)
  return () => {
    query.removeEventListener('change', onStoreChange)
  }
}

function getSnapshot() {
  return window.matchMedia(largeScreenMediaQuery).matches
}

function getServerSnapshot() {
  return false
}

export function AgentConfigYamlPreview({ id, value }: { id: string; value: string }) {
  const isLargeScreen = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot)

  if (!isLargeScreen) return null

  return (
    <div className="min-w-0 lg:sticky lg:top-0 lg:self-start">
      <Field>
        <FieldLabel>
          <FileCode2Icon className="size-4" />
          Config preview
        </FieldLabel>
        <FieldDescription>
          Generated from the builder. Switch to YAML to edit it directly.
        </FieldDescription>
        {value === '' ? (
          <div className="border-border text-muted-foreground flex h-[32rem] items-center justify-center rounded-md border border-dashed px-6 text-center text-sm">
            Fill in the instruction and model to generate the config.
          </div>
        ) : (
          <AgentConfigYamlField
            id={id}
            value={value}
            readOnly
            hideLabel
            className="h-[32rem]"
            onChange={() => undefined}
          />
        )}
      </Field>
    </div>
  )
}
