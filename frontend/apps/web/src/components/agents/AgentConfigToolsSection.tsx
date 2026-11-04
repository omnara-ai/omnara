import type { ToolCatalog } from '@omnara/sdk'
import { zAgentConfigDefinitionTool } from '@omnara/sdk/zod'
import { useMemo, useState } from 'react'

import { AddCustomToolDialog } from '@/components/agents/AddCustomToolDialog'
import {
  type AdvancedConfigEntry,
  configPath,
  parseConfigDocument,
} from '@/components/agents/agentConfigCompatibility'
import { AgentConfigDiagnostics } from '@/components/agents/AgentConfigDiagnostics'
import { editConfigDocument } from '@/components/agents/agentConfigDocument'
import {
  AgentConfigToolRow,
  AgentConfigToolsField,
} from '@/components/agents/AgentConfigToolsField'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigToolsSection({
  catalog,
  form,
}: {
  catalog?: ToolCatalog
  form: AgentBuilderForm
}) {
  const [adding, setAdding] = useState(false)
  return (
    <AgentConfigToolsField
      catalog={catalog}
      tools={form.tools}
      reservedNames={form.reservedToolNames}
      resolvedTools={form.resolvedTools}
      onToolsChange={form.setTools}
      onAddCustomTool={
        catalog
          ? () => {
              setAdding(true)
            }
          : undefined
      }
    >
      {form.advancedEntries
        .filter((entry) => entry.kind === 'custom' && !entry.removed)
        .map((entry) => (
          <CustomToolRow key={configPath(entry.path)} entry={entry} form={form} catalog={catalog} />
        ))}
      {adding && (
        <AddCustomToolDialog
          reservedNames={[
            ...(catalog?.built_in_tools.map((tool) => tool.name) ?? []),
            ...form.tools.map((tool) => tool.name),
            ...form.reservedToolNames,
          ]}
          onClose={() => {
            setAdding(false)
          }}
          onAdd={(entry) => {
            form.setAdvancedEntries([...form.advancedEntries, entry])
            setAdding(false)
          }}
        />
      )}
    </AgentConfigToolsField>
  )
}

function CustomToolRow({
  entry,
  form,
  catalog,
}: {
  entry: AdvancedConfigEntry
  form: AgentBuilderForm
  catalog?: ToolCatalog
}) {
  const [editing, setEditing] = useState(false)
  const [editError, setEditError] = useState<string | null>(null)
  const path = configPath(entry.path)
  const name = entry.path[1] ?? ''
  const original = form.originalAdvancedEntries.find((saved) => configPath(saved.path) === path)
  const issues = form.validationIssues.filter((issue) =>
    entry.path.every((part, index) => issue.path[index] === part),
  )
  const { doc, definition } = useMemo(() => {
    const { doc, diagnostics } = parseConfigDocument(entry.source)
    return {
      doc,
      definition: diagnostics.some((issue) => issue.kind === 'fatal')
        ? undefined
        : zAgentConfigDefinitionTool.safeParse(doc.toJS()).data,
    }
  }, [entry.source])
  const tool = {
    name,
    enabled: definition?.enabled ?? undefined,
    permission: definition?.permission
      ? { mode: definition.permission.mode, parameters: {} }
      : null,
  }
  function update(updated: AdvancedConfigEntry) {
    setEditError(null)
    form.setAdvancedEntries(form.advancedEntries.map((item) => (item === entry ? updated : item)))
  }
  return (
    <div>
      <AgentConfigToolRow
        tool={tool}
        entry={
          catalog
            ? {
                name,
                description: definition?.description ?? '',
                ...catalog.custom_tool_permissions,
              }
            : undefined
        }
        editing={editing}
        onEdit={() => {
          setEditing(!editing)
        }}
        permissionDisabled={issues.length > 0 || !definition}
        onPermissionChange={(mode) => {
          try {
            const transaction = editConfigDocument(entry.source, doc, [['input_schema']])
            if (mode === 'disabled') transaction.set(['enabled'], false)
            else {
              if (definition?.enabled === false) transaction.remove(['enabled'])
              transaction.set(['permission', 'mode'], mode)
            }
            update({ ...entry, source: transaction.serialize() })
          } catch (error) {
            setEditError(
              error instanceof Error ? error.message : 'Cannot safely edit this permission.',
            )
          }
        }}
        onRemove={() => {
          if (original) update({ ...entry, removed: true })
          else form.setAdvancedEntries(form.advancedEntries.filter((item) => item !== entry))
        }}
      />
      {editing && (
        <Field id={`tool-definition-${name}`} className="px-4 pb-4 sm:px-5">
          <FieldLabel htmlFor={`advanced-${path}`}>{path} definition (YAML or JSON)</FieldLabel>
          <p className="text-muted-foreground text-sm">
            The definition is preserved unless you edit it here.
          </p>
          <Textarea
            id={`advanced-${path}`}
            className="min-h-64 font-mono text-xs"
            value={entry.source}
            spellCheck={false}
            aria-invalid={issues.length > 0}
            onChange={(event) => {
              update({ ...entry, source: event.target.value })
            }}
          />
          {original && original.source !== entry.source && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                update(original)
              }}
            >
              Discard edits to {name}
            </Button>
          )}
        </Field>
      )}
      {(issues.length > 0 || editError) && (
        <div className="px-4 pb-3 sm:px-5">
          <AgentConfigDiagnostics issues={issues} />
          {editError && (
            <p role="alert" className="text-destructive text-sm">
              {path}/permission: {editError}
            </p>
          )}
        </div>
      )}
    </div>
  )
}
