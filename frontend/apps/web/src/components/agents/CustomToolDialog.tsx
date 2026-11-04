import { useMemo, useState } from 'react'
import { isMap } from 'yaml'
import { z } from 'zod'

import {
  type AdvancedConfigEntry,
  type ConfigDiagnostic,
  customToolNameError,
  parseConfigDocument,
  validateAdvancedEntry,
} from '@/components/agents/agentConfigCompatibility'
import { AgentConfigDiagnostics } from '@/components/agents/AgentConfigDiagnostics'
import { ConfigEditError, editConfigDocument } from '@/components/agents/agentConfigDocument'
import { configNodeSource } from '@/components/agents/agentConfigSourcePatch'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { Textarea } from '@/components/ui/textarea'

interface ToolFields {
  description: string
  schema: string
}

function toolFields(source: string, parsed = parseConfigDocument(source)): ToolFields | null {
  const { doc, diagnostics } = parsed
  if (!isMap(doc.contents) || diagnostics.some((issue) => issue.kind === 'fatal')) return null
  return {
    description: z.string().safeParse(doc.get('description')).data ?? '',
    schema: configNodeSource(source, doc, ['input_schema']),
  }
}

function evaluateTool(entry: AdvancedConfigEntry, fields: ToolFields | null) {
  try {
    if (fields) {
      const parsed = parseConfigDocument(entry.source)
      const original = toolFields(entry.source, parsed)
      if (!original) throw new Error('Fix the full definition before editing these fields.')
      const { doc } = parsed
      const transaction = editConfigDocument(entry.source, doc, [['input_schema']])
      if (fields.description !== original.description)
        transaction.set(['description'], fields.description)
      if (fields.schema !== original.schema) {
        const { diagnostics } = parseConfigDocument(fields.schema)
        const blockers = diagnostics.filter((issue) => issue.kind === 'fatal')
        if (blockers.length)
          return {
            entry,
            unappliedChanges: true,
            issues: blockers.map((issue) => ({
              ...issue,
              error: true,
              path: [...entry.path, 'input_schema', ...issue.path],
            })),
          }
        transaction.setRawDefinition(['input_schema'], fields.schema)
      }
      entry = { ...entry, source: transaction.serialize() }
    }
    return { entry, unappliedChanges: false, issues: validateAdvancedEntry(entry) }
  } catch (error) {
    return {
      entry,
      unappliedChanges: true,
      issues: [
        {
          path: [
            ...entry.path,
            ...(error instanceof ConfigEditError ? error.path.map(String) : []),
          ],
          kind: 'advanced',
          error: true,
          message: error instanceof Error ? error.message : 'Cannot safely edit this definition.',
        } satisfies ConfigDiagnostic,
      ],
    }
  }
}

export function CustomToolDialog({
  entry,
  configurationDirty,
  reservedNames,
  onSave,
  onClose,
}: {
  entry?: AdvancedConfigEntry
  configurationDirty: boolean
  reservedNames: string[]
  onSave: (entry: AdvancedConfigEntry) => void
  onClose: () => void
}) {
  const [name, setName] = useState(entry?.path[1] ?? '')
  const initialSource =
    entry?.source ??
    "type: custom\ndescription: ''\ninput_schema:\n  type: object\n  properties: {}\n"
  const [source, setSource] = useState(initialSource)
  const [fields, setFields] = useState(() => toolFields(source))
  const rawFields = useMemo(() => toolFields(source), [source])
  const nameError = customToolNameError(
    name,
    reservedNames.filter((reserved) => reserved !== entry?.path[1]),
  )
  const evaluated = useMemo(
    () => evaluateTool({ path: ['tools', name], kind: 'custom', source }, fields),
    [name, source, fields],
  )
  const dirty =
    name !== (entry?.path[1] ?? '') ||
    evaluated.entry.source !== initialSource ||
    evaluated.unappliedChanges
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
          <DialogDescription>Describe the tool and define the inputs it accepts.</DialogDescription>
        </DialogHeader>
        <CustomToolNameField value={name} error={nameError} onChange={setName} />
        <Tabs
          value={fields ? 'fields' : 'raw'}
          onValueChange={(value) => {
            setSource(evaluated.entry.source)
            setFields(value === 'fields' ? toolFields(evaluated.entry.source) : null)
          }}
        >
          <TabsList aria-label="Tool editor">
            <TabsTrigger value="fields" disabled={!fields && !rawFields}>
              Fields
            </TabsTrigger>
            <TabsTrigger value="raw" disabled={!!fields && evaluated.unappliedChanges}>
              Full definition
            </TabsTrigger>
          </TabsList>
          <TabsContent value="fields" className="space-y-4">
            {fields && (
              <>
                <Field>
                  <FieldLabel htmlFor="custom-tool-description">Description</FieldLabel>
                  <Textarea
                    id="custom-tool-description"
                    value={fields.description}
                    rows={3}
                    placeholder="Explain when the agent should use this tool."
                    onChange={(event) => {
                      setFields({ ...fields, description: event.target.value })
                    }}
                  />
                </Field>
                <Field>
                  <FieldLabel htmlFor="custom-tool-schema">Input schema (YAML or JSON)</FieldLabel>
                  <p className="text-muted-foreground text-sm">
                    JSON Schema describing the arguments the tool accepts. Advanced schema features
                    are preserved.
                  </p>
                  <Textarea
                    id="custom-tool-schema"
                    className="min-h-48 font-mono text-xs"
                    value={fields.schema}
                    spellCheck={false}
                    aria-invalid={evaluated.issues.some(
                      (issue) => issue.path[2] === 'input_schema',
                    )}
                    onChange={(event) => {
                      setFields({ ...fields, schema: event.target.value })
                    }}
                  />
                </Field>
              </>
            )}
          </TabsContent>
          <TabsContent value="raw">
            <Field>
              <FieldLabel htmlFor="custom-tool-definition">Definition (YAML or JSON)</FieldLabel>
              <Textarea
                id="custom-tool-definition"
                className="min-h-64 font-mono text-xs"
                value={source}
                spellCheck={false}
                aria-invalid={evaluated.issues.length > 0}
                onChange={(event) => {
                  setSource(event.target.value)
                }}
              />
            </Field>
          </TabsContent>
        </Tabs>
        {name !== '' && <AgentConfigDiagnostics issues={evaluated.issues} />}
        <DialogFooter>
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="button"
            disabled={!!nameError || evaluated.issues.length > 0}
            onClick={() => {
              onSave(evaluated.entry)
            }}
          >
            {entry ? 'Save tool' : 'Add custom tool'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

function CustomToolNameField({
  value,
  error,
  onChange,
}: {
  value: string
  error?: string
  onChange: (value: string) => void
}) {
  const visibleError = value === '' ? undefined : error
  return (
    <Field>
      <FieldLabel htmlFor="custom-tool-name">Tool name</FieldLabel>
      <Input
        id="custom-tool-name"
        value={value}
        placeholder="search_records"
        aria-invalid={!!visibleError}
        aria-describedby={visibleError ? 'custom-tool-name-error' : undefined}
        onChange={(event) => {
          onChange(event.target.value)
        }}
      />
      {visibleError && (
        <p id="custom-tool-name-error" role="alert" className="text-destructive text-sm">
          {visibleError}
        </p>
      )}
    </Field>
  )
}
