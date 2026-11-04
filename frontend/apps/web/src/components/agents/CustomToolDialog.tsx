import { useMemo, useState } from 'react'

import {
  type AdvancedConfigEntry,
  customToolNameError,
  validateAdvancedEntry,
} from '@/components/agents/agentConfigCompatibility'
import { AgentConfigDiagnostics } from '@/components/agents/AgentConfigDiagnostics'
import type { ConfigSourceFormat } from '@/components/agents/agentConfigSourceFormat'
import { useAgentUnsavedChangesWarning } from '@/components/agents/useAgentUnsavedChangesWarning'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

const templates = {
  yaml: "type: custom\ndescription: ''\ninput_schema:\n  type: object\n  properties: {}\n",
  json: JSON.stringify(
    { type: 'custom', description: '', input_schema: { type: 'object', properties: {} } },
    null,
    2,
  ),
}

export function CustomToolDialog({
  entry,
  format,
  configurationDirty,
  reservedNames,
  onSave,
  onClose,
}: {
  entry?: AdvancedConfigEntry
  format: ConfigSourceFormat
  configurationDirty: boolean
  reservedNames: string[]
  onSave: (entry: AdvancedConfigEntry) => void
  onClose: () => void
}) {
  const initialName = entry?.path[1] ?? ''
  const initialSource = entry?.source ?? templates[format]
  const [name, setName] = useState(initialName)
  const [source, setSource] = useState(initialSource)
  const nameError = customToolNameError(
    name,
    reservedNames.filter((reserved) => reserved !== initialName),
  )
  const visibleNameError = name === '' ? undefined : nameError
  const definition = useMemo<AdvancedConfigEntry>(
    () => ({ path: ['tools', name], kind: 'custom', source }),
    [name, source],
  )
  const issues = useMemo(() => validateAdvancedEntry(definition, format), [definition, format])
  const dirty = name !== initialName || source !== initialSource
  useAgentUnsavedChangesWarning(dirty && !configurationDirty)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent
        className="sm:max-w-2xl"
        onInteractOutside={(event) => {
          if (dirty) event.preventDefault()
        }}
      >
        <DialogHeader>
          <DialogTitle>{entry ? 'Edit custom tool' : 'Add custom tool'}</DialogTitle>
          <DialogDescription>Define the tool using {format.toUpperCase()}.</DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel htmlFor="custom-tool-name">Tool name</FieldLabel>
          <Input
            id="custom-tool-name"
            value={name}
            placeholder="search_records"
            aria-invalid={!!visibleNameError}
            aria-describedby={visibleNameError ? 'custom-tool-name-error' : undefined}
            onChange={(event) => {
              setName(event.target.value)
            }}
          />
          {visibleNameError && (
            <p id="custom-tool-name-error" role="alert" className="text-destructive text-sm">
              {visibleNameError}
            </p>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor="custom-tool-definition">Definition</FieldLabel>
          <Textarea
            id="custom-tool-definition"
            className="max-h-96 min-h-64 font-mono text-xs"
            value={source}
            spellCheck={false}
            aria-invalid={issues.length > 0}
            onChange={(event) => {
              setSource(event.target.value)
            }}
          />
        </Field>
        {name !== '' && <AgentConfigDiagnostics issues={issues} />}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={!!nameError || issues.length > 0}
            onClick={() => {
              onSave(definition)
            }}
          >
            {entry ? 'Save tool' : 'Add custom tool'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
