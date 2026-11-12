import { useMemo, useRef, useState } from 'react'
import { Document, isMap } from 'yaml'

import {
  type AdvancedConfigEntry,
  type ConfigDiagnostic,
  customToolNameError,
  parseConfigDocument,
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

const emptyTool = {
  type: 'custom',
  description: '',
  input_schema: { type: 'object', properties: {} },
}

export function CustomToolDialog({
  entry,
  format,
  configurationDirty,
  configurationSource,
  reservedNames,
  onSave,
  onClose,
}: {
  entry?: AdvancedConfigEntry
  format: ConfigSourceFormat
  configurationDirty: boolean
  configurationSource: string
  reservedNames: string[]
  onSave: (entry: AdvancedConfigEntry) => ConfigDiagnostic[]
  onClose: () => void
}) {
  const initialName = entry?.path[1] ?? ''
  const [initialSource] = useState(() => {
    if (entry) return entry.source
    if (format === 'json') return JSON.stringify(emptyTool, null, 2)
    const { doc } = parseConfigDocument(configurationSource)
    const tools = doc.get('tools', true)
    const flow =
      (isMap(doc.contents) && doc.contents.flow === true) ||
      (isMap(tools) && tools.flow === true && tools.items.length > 0)
    return new Document(emptyTool).toString({ collectionStyle: flow ? 'flow' : 'block' })
  })
  const [name, setName] = useState(initialName)
  const [source, setSource] = useState(initialSource)
  const [submitted, setSubmitted] = useState(false)
  const [saveIssues, setSaveIssues] = useState<ConfigDiagnostic[]>([])
  const nameRef = useRef<HTMLInputElement>(null)
  const definitionRef = useRef<HTMLTextAreaElement>(null)
  const nameError = customToolNameError(
    name,
    reservedNames.filter((reserved) => reserved !== initialName),
  )
  const visibleNameError = submitted ? nameError : undefined
  const definition = useMemo<AdvancedConfigEntry>(
    () => ({ path: ['tools', name], kind: 'custom', source }),
    [name, source],
  )
  const issues = useMemo(() => validateAdvancedEntry(definition, format), [definition, format])
  const visibleIssues = [...issues, ...saveIssues]
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
        onEscapeKeyDown={(event) => {
          if (dirty) event.preventDefault()
        }}
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
            ref={nameRef}
            id="custom-tool-name"
            value={name}
            placeholder="search_records"
            aria-invalid={!!visibleNameError}
            aria-describedby={visibleNameError ? 'custom-tool-name-error' : undefined}
            onChange={(event) => {
              setName(event.target.value)
              setSaveIssues([])
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
            ref={definitionRef}
            id="custom-tool-definition"
            className="max-h-96 min-h-64 font-mono text-xs"
            value={source}
            spellCheck={false}
            aria-invalid={submitted && visibleIssues.length > 0}
            aria-describedby={
              submitted && visibleIssues.length > 0 ? 'custom-tool-definition-errors' : undefined
            }
            onChange={(event) => {
              setSource(event.target.value)
              setSaveIssues([])
            }}
          />
        </Field>
        {submitted && visibleIssues.length > 0 && (
          <div id="custom-tool-definition-errors">
            <AgentConfigDiagnostics issues={visibleIssues} basePath={definition.path} />
          </div>
        )}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            onClick={() => {
              setSubmitted(true)
              if (nameError || issues.length > 0) {
                const invalidField = nameError ? nameRef.current : definitionRef.current
                invalidField?.focus()
                return
              }
              const errors = onSave(definition)
              if (errors.length > 0) {
                setSaveIssues(errors)
                definitionRef.current?.focus()
              }
            }}
          >
            {entry ? 'Save tool' : 'Add custom tool'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
