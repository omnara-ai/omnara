import type { ToolCatalog } from '@omnara/sdk'
import { zAgentConfigDefinitionTool } from '@omnara/sdk/zod'
import { useMemo, useState } from 'react'

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
import { CustomToolDialog } from '@/components/agents/CustomToolDialog'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'

export function AgentConfigToolsSection({
  catalog,
  form,
}: {
  catalog?: ToolCatalog
  form: AgentBuilderForm
}) {
  const [editing, setEditing] = useState<AdvancedConfigEntry | 'new' | null>(null)
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
              setEditing('new')
            }
          : undefined
      }
    >
      {form.advancedEntries
        .filter((entry) => entry.kind === 'custom' && !entry.removed)
        .map((entry) => (
          <CustomToolRow
            key={configPath(entry.path)}
            entry={entry}
            form={form}
            catalog={catalog}
            onEdit={() => {
              setEditing(entry)
            }}
          />
        ))}
      {editing && (
        <CustomToolDialog
          entry={editing === 'new' ? undefined : editing}
          configurationDirty={form.dirty}
          format={form.sourceFormat}
          reservedNames={[
            ...(catalog?.built_in_tools.map((tool) => tool.name) ?? []),
            ...form.tools.map((tool) => tool.name),
            ...form.advancedEntries
              .filter((entry) => entry.path[0] === 'tools' && !entry.removed)
              .map((entry) => entry.path[1] ?? ''),
          ]}
          onClose={() => {
            setEditing(null)
          }}
          onSave={(updated) => {
            const target = configPath(updated.path)
            const entries = form.advancedEntries.flatMap((item) => {
              if (configPath(item.path) === target) return updated
              if (item !== editing) return item
              return form.originalAdvancedEntries.some(
                (saved) => configPath(saved.path) === configPath(item.path),
              )
                ? { ...item, removed: true }
                : []
            })
            if (!entries.some((item) => configPath(item.path) === target)) entries.push(updated)
            form.setAdvancedEntries(entries)
            setEditing(null)
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
  onEdit,
}: {
  entry: AdvancedConfigEntry
  form: AgentBuilderForm
  catalog?: ToolCatalog
  onEdit: () => void
}) {
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
        onEdit={onEdit}
        permissionDisabled={issues.length > 0 || !definition}
        onPermissionChange={(mode) => {
          try {
            const transaction = editConfigDocument(
              entry.source,
              doc,
              [['input_schema']],
              form.sourceFormat,
            )
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
      {(issues.length > 0 || editError) && (
        <div className="px-4 pb-3 sm:px-5">
          <AgentConfigDiagnostics issues={issues} basePath={entry.path} />
          {editError && (
            <p role="alert" className="text-destructive text-sm">
              Permission: {editError}
            </p>
          )}
        </div>
      )}
    </div>
  )
}
