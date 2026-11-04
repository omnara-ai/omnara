import { useState } from 'react'

import {
  type AdvancedConfigEntry,
  customToolNameError,
  validateAdvancedEntry,
} from '@/components/agents/agentConfigCompatibility'
import { AgentConfigDiagnostics } from '@/components/agents/AgentConfigDiagnostics'
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

export function AddCustomToolDialog({
  reservedNames,
  onAdd,
  onClose,
}: {
  reservedNames: string[]
  onAdd: (entry: AdvancedConfigEntry) => void
  onClose: () => void
}) {
  const [name, setName] = useState('')
  const [source, setSource] = useState(
    'type: custom\ndescription: Describe what this tool does.\ninput_schema:\n  type: object\n  properties: {}\n',
  )
  const entry: AdvancedConfigEntry = { path: ['tools', name], kind: 'custom', source }
  const nameError = customToolNameError(name, reservedNames)
  const issues = validateAdvancedEntry(entry)
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>Add custom tool</DialogTitle>
          <DialogDescription>
            Name your tool and define its description and input schema in YAML or JSON.
          </DialogDescription>
        </DialogHeader>
        <Field>
          <FieldLabel htmlFor="custom-tool-name">Tool name</FieldLabel>
          <Input
            id="custom-tool-name"
            value={name}
            placeholder="search_records"
            aria-invalid={name !== '' && !!nameError}
            aria-describedby={name !== '' && nameError ? 'custom-tool-name-error' : undefined}
            onChange={(event) => {
              setName(event.target.value)
            }}
          />
          {name !== '' && nameError && (
            <p id="custom-tool-name-error" role="alert" className="text-destructive text-sm">
              {nameError}
            </p>
          )}
        </Field>
        <Field>
          <FieldLabel htmlFor="custom-tool-definition">Definition (YAML or JSON)</FieldLabel>
          <Textarea
            id="custom-tool-definition"
            className="min-h-64 font-mono text-xs"
            value={source}
            spellCheck={false}
            aria-invalid={issues.length > 0}
            onChange={(event) => {
              setSource(event.target.value)
            }}
          />
          <AgentConfigDiagnostics issues={issues} />
        </Field>
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={!!nameError || issues.length > 0}
            onClick={() => {
              onAdd(entry)
            }}
          >
            Add custom tool
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
